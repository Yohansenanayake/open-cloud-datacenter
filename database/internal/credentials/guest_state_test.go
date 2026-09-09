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
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestGuestStateCloudInitAndRepave(t *testing.T) {
	for _, initialize := range []bool{true, false} {
		p := testBootstrapParams()
		p.InstanceUID = "instance-uid"
		p.GuestStatePVCUID = "pvc-uid"
		p.InitializeGuestState = initialize
		data, _ := BuildCloudInit(p, testMaterial())
		var doc struct {
			Files []struct{ Path, Content, Encoding string } `json:"write_files"`
		}
		if err := yaml.Unmarshal([]byte(data), &doc); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for _, f := range doc.Files {
			b := []byte(f.Content)
			if f.Encoding == "b64" {
				var err error
				b, err = base64.StdEncoding.DecodeString(f.Content)
				if err != nil {
					t.Fatal(err)
				}
			}
			files[f.Path] = string(b)
		}
		env := files["/etc/dbaas/guest-state.env"]
		want := "INITIALIZE_STATE=false"
		if initialize {
			want = "INITIALIZE_STATE=true"
		}
		if !strings.Contains(env, want) || !strings.Contains(env, "STATE_PVC_UID='pvc-uid'") {
			t.Fatalf("env: %s", env)
		}
		bootstrap := files["/etc/dbaas/bootstrap.sh"]
		if i, j := strings.Index(bootstrap, "/usr/local/sbin/dbaas-guest-state initialize"), strings.Index(bootstrap, "pg_createcluster"); i < 0 || j < 0 || i > j {
			t.Fatal("state validation must precede PostgreSQL bootstrap")
		}
		if !strings.Contains(files["/etc/systemd/system/dbaas-guest-state.service"], "ExecStart=/usr/local/sbin/dbaas-guest-state mount") {
			t.Fatal("reboots must only mount")
		}
		for _, path := range []string{"/usr/local/sbin/dbaas-guest-state", "/etc/dbaas/bootstrap.sh"} {
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(files[path])
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s syntax: %s: %v", path, out, err)
			}
		}
	}
}

// Exercise the actual shell admission/initialization logic with block-device
// operations substituted. Real mount, udev and repave behavior needs VM tests.
func TestGuestStateInitializationAndRecoveryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, fs, mode, identity      string
		allow, attempt, missing, fail bool
	}{
		{name: "initial", fs: "blank", mode: "initialize", allow: true},
		{name: "reboot", fs: "ext4", mode: "mount", identity: "instance\npvc"},
		{name: "repave", fs: "ext4", mode: "initialize", identity: "instance\npvc"},
		{name: "empty-established", fs: "blank", mode: "mount", fail: true},
		{name: "empty-repave", fs: "blank", mode: "initialize", fail: true},
		{name: "interrupted-format", fs: "blank", mode: "initialize", allow: true, attempt: true, fail: true},
		{name: "missing-identity", fs: "ext4", mode: "initialize", allow: true, fail: true},
		{name: "wrong-identity", fs: "ext4", mode: "mount", identity: "other\npvc", fail: true},
		{name: "wrong-pvc", fs: "ext4", mode: "mount", identity: "instance\nother", fail: true},
		{name: "wrong-filesystem", fs: "xfs", mode: "initialize", allow: true, fail: true},
		{name: "io-error", fs: "io-error", mode: "initialize", allow: true, fail: true},
		{name: "missing-disk", fs: "ext4", mode: "mount", missing: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "helper.sh")
			if err := os.WriteFile(script, []byte(guestStateScript), 0600); err != nil {
				t.Fatal(err)
			}
			mount := filepath.Join(dir, "state")
			if err := os.Mkdir(mount, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.identity != "" {
				if err := os.WriteFile(filepath.Join(mount, "volume-identity"), []byte(tc.identity+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.attempt {
				if err := os.WriteFile(filepath.Join(dir, "attempt"), []byte("pvc"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			body := `source "$1"
STATE_MOUNT="$2/state"
ATTEMPT_MARKER="$2/attempt"
INSTANCE_UID=instance
STATE_PVC_UID=pvc
FS="$3"
INITIALIZE_STATE="$4"
MISSING="$5"
is_block_device() { [ "$MISSING" = false ]; }
filesystem_type() { case "$FS" in blank) return 2;; io-error) return 8;; *) echo "$FS";; esac; }
mount_state() { :; }
install() { command install -d -m 0700 "${@: -1}"; }
wipefs() { :; }
mkfs.ext4() { touch "$STATE_MOUNT/formatted"; }
# Simulate the VM's root owner while preserving actual file permission checks.
stat() { command stat -c '0:0:%a' "${@: -1}"; }
prepare_state "$6"
`
			boolString := func(b bool) string {
				if b {
					return "true"
				}
				return "false"
			}
			cmd := exec.Command("bash", "-c", body, "test", script, dir, tc.fs, boolString(tc.allow), boolString(tc.missing), tc.mode)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v, want failure=%v; %s", err, tc.fail, out)
			}
			_, formatErr := os.Stat(filepath.Join(mount, "formatted"))
			if tc.name == "initial" {
				if formatErr != nil {
					t.Fatal("fresh disk was not initialized")
				}
				identity, err := os.ReadFile(filepath.Join(mount, "volume-identity"))
				if err != nil || string(identity) != "instance\npvc\n" {
					t.Fatalf("identity=%q err=%v", identity, err)
				}
			} else if formatErr == nil {
				t.Fatal("existing or unsafe disk was formatted")
			}
		})
	}
}

func TestGuestStateVerifiesMountedDevice(t *testing.T) {
	for _, tc := range []struct {
		name, source, fs, options string
		fail                      bool
	}{
		{"valid", "expected", "ext4", "rw,nosuid,nodev,noexec", false},
		{"wrong-device", "other", "ext4", "rw", true},
		{"wrong-filesystem", "expected", "xfs", "rw", true},
		{"read-only", "expected", "ext4", "ro,nosuid,nodev", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "helper.sh")
			if err := os.WriteFile(script, []byte(guestStateScript), 0600); err != nil {
				t.Fatal(err)
			}
			body := `source "$1"
STATE_DEVICE="$2/expected"
STATE_MOUNT="$2/state"
ACTUAL="$2/$3"
FSTYPE="$4"
OPTIONS="$5"
mountpoint() { return 0; }
findmnt() { case "${@: -1}" in SOURCE) echo "$ACTUAL";; FSTYPE) echo "$FSTYPE";; OPTIONS) echo "$OPTIONS";; esac; }
mount_state
`
			out, err := exec.Command("bash", "-c", body, "test", script, dir, tc.source, tc.fs, tc.options).CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v expected failure=%v: %s", err, tc.fail, out)
			}
		})
	}
}

func TestGuestStateSetupIsNeverSkipped(t *testing.T) {
	// Missing identity must reach the helper's failure check, not bypass it.
	files, commands := guestStateCloudInit(BootstrapParams{})
	if !strings.Contains(files, "/usr/local/sbin/dbaas-guest-state") || !strings.Contains(commands, "/usr/local/sbin/dbaas-guest-state initialize") {
		t.Fatal("guest-state validation was skipped for missing identity")
	}
}
