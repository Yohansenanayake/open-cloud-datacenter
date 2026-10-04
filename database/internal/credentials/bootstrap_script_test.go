/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package credentials

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests execute the rendered bootstrap.sh for real, under bash, in a
// sandbox: every absolute path the script touches is rehomed under a temp
// dir, and the system commands it calls are replaced by stubs that record
// their invocations. The restore branch is the part of bootstrap where a
// mistake means formatting a restored disk or exposing the source's
// credentials — string-matching the template can't prove its control flow;
// running it does.

const sandboxPGVer = "17"

// stubs are the commands bootstrap.sh calls that need a real VM. Each logs
// "<name> <args>" to $SANDBOX/calls; behavior is steered by env vars.
var stubs = map[string]string{
	"systemctl": `echo "systemctl $*" >> "$SANDBOX/calls"
if [ "$1" = "restart" ] && [ "$2" = "postgresql" ]; then
  conf="$SANDBOX/etc/postgresql/` + sandboxPGVer + `/main"
  echo "restart listen=$(grep -o "^listen_addresses = '[^']*'" "$conf/postgresql.conf") hostssl=$(grep -c '^hostssl' "$conf/pg_hba.conf" || true)" >> "$SANDBOX/calls"
fi`,
	"pg_lsclusters":  `true`,
	"pg_dropcluster": `echo "pg_dropcluster $*" >> "$SANDBOX/calls"`,
	"pg_createcluster": `echo "pg_createcluster $*" >> "$SANDBOX/calls"
conf="$SANDBOX/etc/postgresql/` + sandboxPGVer + `/main"
mkdir -p "$conf"
printf "#listen_addresses = 'localhost'\n#port = 5432\n#max_connections = 100\n#ssl = off\n" > "$conf/postgresql.conf"
printf "local all postgres peer\n" > "$conf/pg_hba.conf"`,
	"findmnt": `exit 1`,
	"blkid": `[ "${BLKID_FS:-0}" = "1" ] || [ -f "$SANDBOX/formatted" ] || exit 2
[ "$2" = "-s" ] && echo "uuid-1234"
exit 0`,
	"mkfs.ext4": `echo "mkfs.ext4 $*" >> "$SANDBOX/calls"; touch "$SANDBOX/formatted"`,
	"mount":     `echo "mount $*" >> "$SANDBOX/calls"`,
	"umount":    `echo "umount $*" >> "$SANDBOX/calls"`,
	"chown":     `true`,
	"cp":        `echo "cp $*" >> "$SANDBOX/calls"`,
	"pg_isready": `[ "${PGISREADY_FAIL:-0}" = "1" ] && exit 1
exit 0`,
	// Real time must pass for bash's SECONDS to advance; keep it short.
	"sleep": `exec "$(PATH=/usr/bin:/bin command -v sleep)" 1`,
	"sudo":  `shift 2; exec "$@"`,
	"psql": `if [ -t 0 ] || [ "$*" != "${*#*-tAc}" ]; then
  q="${@: -1}"
  case "$q" in
    *pg_database*) [ "${PSQL_HAS_DB:-1}" = "1" ] && echo 1 ;;
    *pg_roles*)    [ "${PSQL_HAS_ROLE:-1}" = "1" ] && echo 1 ;;
  esac
  exit 0
fi
sql=$(cat)
printf "psql %s\n" "$sql" >> "$SANDBOX/calls"
case "$sql" in *"ALTER ROLE"*) [ "${PSQL_FAIL_ALTER:-0}" = "1" ] && exit 3 ;; esac
exit 0`,
}

type sandboxRun struct {
	root  string
	err   error
	calls string
}

func (s sandboxRun) exists(path string) bool {
	_, err := os.Stat(filepath.Join(s.root, path))
	return err == nil
}

func (s sandboxRun) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.root, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// extractBootstrapScript pulls bootstrap.sh out of the rendered cloud-config
// (its write_files content block, dedented).
func extractBootstrapScript(t *testing.T, userdata string) string {
	t.Helper()
	start := strings.Index(userdata, "  - path: /etc/dbaas/bootstrap.sh")
	body := userdata[start:]
	body = body[strings.Index(body, "content: |\n")+len("content: |\n"):]
	body = body[:strings.Index(body, "\nruncmd:")]
	var out strings.Builder
	for _, line := range strings.Split(body, "\n") {
		out.WriteString(strings.TrimPrefix(line, "      "))
		out.WriteString("\n")
	}
	return out.String()
}

// sandboxDisk describes the data disk the script finds.
type sandboxDisk struct {
	attached   bool
	filesystem bool
	pgVersion  string // "" = no cluster on the disk
	marker     string // restore marker already on the disk ("" = none)
}

func runBootstrap(t *testing.T, restoreID string, disk sandboxDisk, env ...string) sandboxRun {
	t.Helper()
	p := testBootstrapParams()
	p.RestoreID = restoreID
	return runBootstrapParams(t, p, disk, env...)
}

func runBootstrapParams(t *testing.T, p BootstrapParams, disk sandboxDisk, env ...string) sandboxRun {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	p.EngineVersion = sandboxPGVer
	userdata, _ := BuildCloudInit(p, testMaterial())

	// bootstrap.env as cloud-init would write it (the values are embedded
	// in userdata; reuse the rendered block verbatim).
	envStart := strings.Index(userdata, "    content: |\n") + len("    content: |\n")
	envBlock := userdata[envStart:strings.Index(userdata, "  - path: /etc/ssl/certs/pg-ca.crt")]
	var envFile strings.Builder
	for _, line := range strings.Split(strings.TrimRight(envBlock, "\n"), "\n") {
		envFile.WriteString(strings.TrimPrefix(line, "      ") + "\n")
	}

	script := extractBootstrapScript(t, userdata)
	for _, prefix := range []string{"/etc/", "/var/lib/", "/mnt/", "/dev/vdb"} {
		script = strings.ReplaceAll(script, prefix, root+prefix)
	}
	script = strings.ReplaceAll(script, `[ -b "${PGDATA_DEVICE}" ]`, `[ -e "${PGDATA_DEVICE}" ]`)

	mustWrite := func(path, content string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("etc/dbaas/bootstrap.env", envFile.String())
	mustWrite("etc/dbaas/bootstrap.sh", script)
	mustWrite("etc/fstab", "")
	mustWrite("etc/ssl/private/pg-server.key", "key")
	mustWrite("etc/default/.keep", "")
	mustWrite("var/lib/dbaas/.keep", "")
	mustWrite("var/lib/postgresql/.keep", "")
	mustWrite("mnt/dbaas-pgdata/.keep", "")
	if disk.attached {
		mustWrite("dev/vdb", "")
	}
	if disk.pgVersion != "" {
		mustWrite("mnt/dbaas-pgdata/"+sandboxPGVer+"/main/PG_VERSION", disk.pgVersion+"\n")
	}
	if disk.marker != "" {
		mustWrite("var/lib/postgresql/.dbaas-restored-from", disk.marker+"\n")
	}
	bin := filepath.Join(root, "stubbin")
	for name, body := range stubs {
		mustWrite(filepath.Join("stubbin", name), "#!/bin/bash\n"+body+"\n")
	}

	cmd := exec.Command("bash", filepath.Join(root, "etc/dbaas/bootstrap.sh"))
	cmd.Env = append([]string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"SANDBOX=" + root,
		"BLKID_FS=" + map[bool]string{true: "1", false: "0"}[disk.filesystem],
	}, env...)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(filepath.Join(root, "calls"))
	t.Logf("bootstrap output:\n%s\ncalls:\n%s", out, calls)
	return sandboxRun{root: root, err: err, calls: string(calls)}
}

// restoredDisk is a correctly restored data disk.
var restoredDisk = sandboxDisk{attached: true, filesystem: true, pgVersion: sandboxPGVer}

func TestBootstrapRestoreHappyPath(t *testing.T) {
	run := runBootstrap(t, "restore-uid-1", restoredDisk)

	if run.err != nil {
		t.Fatalf("bootstrap failed: %v", run.err)
	}
	if strings.Contains(run.calls, "mkfs.ext4") || strings.Contains(run.calls, "cp ") {
		t.Fatal("a restored disk must never be formatted or overwritten")
	}
	alter := strings.Index(run.calls, "ALTER ROLE \"dbadmin\" WITH LOGIN PASSWORD 'admin-pw'")
	if alter == -1 || !strings.Contains(run.calls, "ALTER ROLE postgres_exporter WITH LOGIN PASSWORD 'exporter-pw'") {
		t.Fatal("the DBaaS-managed roles must be given this instance's credentials")
	}
	// Remote access: closed on the first restart, opened only after the
	// credential reset.
	firstRestart := strings.Index(run.calls, "restart listen=")
	if !strings.HasPrefix(run.calls[firstRestart:], "restart listen=listen_addresses = 'localhost' hostssl=0") {
		t.Fatalf("first restart must be localhost-only with no hostssl rules:\n%s", run.calls[firstRestart:])
	}
	if !strings.Contains(run.calls[alter:], "restart listen=listen_addresses = '*' hostssl=2") {
		t.Fatal("remote SSL access must be opened after, and only after, the credential reset")
	}
	if strings.Contains(run.calls[:alter], "listen_addresses = '*'") {
		t.Fatal("remote access was opened before the credential reset")
	}
	if got := strings.TrimSpace(run.read(t, "var/lib/postgresql/.dbaas-restored-from")); got != "restore-uid-1" {
		t.Fatalf("data-disk restore marker = %q, want restore-uid-1", got)
	}
	if !run.exists("var/lib/dbaas/bootstrap-complete") {
		t.Fatal("readiness marker must be written once the restore is verified")
	}
	if run.exists("etc/dbaas/bootstrap.env") {
		t.Fatal("bootstrap.env must be shredded")
	}
}

func TestBootstrapRestoreFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		disk sandboxDisk
		env  []string
		want string
	}{
		{"disk not attached", sandboxDisk{}, nil, "is not attached"},
		{"no filesystem", sandboxDisk{attached: true}, nil, "refusing to format"},
		{"no cluster on disk", sandboxDisk{attached: true, filesystem: true}, nil, "holds no PostgreSQL 17 cluster"},
		{"wrong version", sandboxDisk{attached: true, filesystem: true, pgVersion: "16"}, nil, "does not match engine version 17"},
		{"database missing", restoredDisk, []string{"PSQL_HAS_DB=0"}, "has no database orders"},
		{"master role missing", restoredDisk, []string{"PSQL_HAS_ROLE=0"}, "has no role dbadmin"},
		{"credential reset fails", restoredDisk, []string{"PSQL_FAIL_ALTER=1"}, "could not reset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runBootstrap(t, "restore-uid-1", tc.disk, tc.env...)

			if run.err == nil {
				t.Fatal("bootstrap must fail")
			}
			if !run.exists("var/lib/dbaas/restore-failed") || !strings.Contains(run.read(t, "var/lib/dbaas/restore-failed"), tc.want) {
				t.Fatalf("restore-failed must record %q", tc.want)
			}
			if run.exists("var/lib/dbaas/bootstrap-complete") {
				t.Fatal("a failed restore must never report ready")
			}
			if strings.Contains(run.calls, "mkfs.ext4") || strings.Contains(run.calls, "cp ") {
				t.Fatal("a failed restore must never format or overwrite the disk")
			}
			if strings.Contains(run.calls, "listen_addresses = '*'") {
				t.Fatal("a failed restore must never open remote access")
			}
			if run.exists("etc/dbaas/bootstrap.env") {
				t.Fatal("secrets must be shredded on failure too")
			}
		})
	}
}

// A repave or VM recreation of an already-restored instance (the data disk's
// marker names this restore) must not redo the restore — in particular not
// reset a master password the tenant has since changed.
func TestBootstrapAlreadyRestoredDiskSkipsRestoreWork(t *testing.T) {
	disk := restoredDisk
	disk.marker = "restore-uid-1"
	run := runBootstrap(t, "restore-uid-1", disk)

	if run.err != nil {
		t.Fatalf("bootstrap failed: %v", run.err)
	}
	if strings.Contains(run.calls, "ALTER ROLE") {
		t.Fatal("credentials must not be reset again once this restore is recorded on the disk")
	}
	if !strings.Contains(run.calls, "restart listen=listen_addresses = '*' hostssl=2") {
		t.Fatal("an already-restored instance opens remote access normally")
	}
	if strings.Contains(run.calls, "mkfs.ext4") {
		t.Fatal("still must never format a restored instance's disk")
	}
}

// A disk restored from a snapshot of a previously restored instance carries
// that earlier restore's marker — a new restore must still do its work.
func TestBootstrapStaleMarkerFromEarlierRestoreStillRestores(t *testing.T) {
	disk := restoredDisk
	disk.marker = "an-earlier-restore-uid"
	run := runBootstrap(t, "restore-uid-1", disk)

	if run.err != nil {
		t.Fatalf("bootstrap failed: %v", run.err)
	}
	if !strings.Contains(run.calls, "ALTER ROLE") {
		t.Fatal("a new restore must reset credentials even if the disk records an earlier restore")
	}
	if got := strings.TrimSpace(run.read(t, "var/lib/postgresql/.dbaas-restored-from")); got != "restore-uid-1" {
		t.Fatalf("marker = %q, want updated to restore-uid-1", got)
	}
}

// The ordinary (non-restore) path is unchanged: a blank disk is formatted
// and remote access opens on the first restart.
func TestBootstrapOrdinaryInstanceUnchanged(t *testing.T) {
	run := runBootstrap(t, "", sandboxDisk{attached: true})

	if run.err != nil {
		t.Fatalf("bootstrap failed: %v", run.err)
	}
	if !strings.Contains(run.calls, "mkfs.ext4") {
		t.Fatal("an ordinary instance's blank disk is formatted")
	}
	if !strings.HasPrefix(run.calls[strings.Index(run.calls, "restart listen="):], "restart listen=listen_addresses = '*' hostssl=2") {
		t.Fatal("an ordinary instance opens remote access on the first restart")
	}
	if strings.Contains(run.calls, "ALTER ROLE") || run.exists("var/lib/postgresql/.dbaas-restored-from") {
		t.Fatal("no restore work for an ordinary instance")
	}
	if !run.exists("var/lib/dbaas/bootstrap-complete") {
		t.Fatal("readiness marker must be written")
	}
}

// A restore whose PostgreSQL never finishes recovering fails once the
// configured restore.recoveryTimeout has passed — closed, like every other
// restore failure.
func TestBootstrapRestoreFailsAfterConfiguredRecoveryTimeout(t *testing.T) {
	p := testBootstrapParams()
	p.RestoreID = "restore-uid-1"
	p.RestoreRecoveryTimeout = time.Second
	start := time.Now()

	run := runBootstrapParams(t, p, restoredDisk, "PGISREADY_FAIL=1")

	if run.err == nil {
		t.Fatal("bootstrap must fail when recovery doesn't finish in time")
	}
	if !strings.Contains(run.read(t, "var/lib/dbaas/restore-failed"), "within 1s") {
		t.Fatalf("restore-failed = %q, want the configured timeout in the reason", run.read(t, "var/lib/dbaas/restore-failed"))
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("took %v: the configured 1s timeout wasn't honoured", elapsed)
	}
	if run.exists("var/lib/dbaas/bootstrap-complete") || strings.Contains(run.calls, "listen_addresses = '*'") {
		t.Fatal("a timed-out restore must never report ready or open remote access")
	}
}
