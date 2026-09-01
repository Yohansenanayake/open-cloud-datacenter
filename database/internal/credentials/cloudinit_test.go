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
	"strings"
	"testing"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

func testMaterial() *Material {
	return &Material{
		AdminUser:        "dbadmin",
		AdminPassword:    "admin-pw",
		ReplPassword:     "repl-pw",
		ExporterPassword: "exporter-pw",
		TLS: &TLSBundle{
			CACertPEM:     "CA-CERT-PEM",
			CAKeyPEM:      "CA-KEY-PEM",
			ServerCertPEM: "SERVER-CERT-PEM",
			ServerKeyPEM:  "SERVER-KEY-PEM",
		},
	}
}

func testBootstrapParams() BootstrapParams {
	return BootstrapParams{
		ID:             "orders",
		DBName:         "orders",
		Port:           5432,
		MasterUser:     "dbadmin",
		MaxConnections: 100,
	}
}

func TestBuildCloudInitEmbedsBootstrapAndTLSMaterial(t *testing.T) {
	userdata, _ := BuildCloudInit(testBootstrapParams(), testMaterial())

	for _, want := range []string{
		"INSTANCE_ID=orders",
		"DB_NAME=orders",
		"DB_PORT=5432",
		"MASTER_USER=dbadmin",
		"MASTER_PASSWORD=admin-pw",
		"REPL_PASSWORD=repl-pw",
		"EXPORTER_PASSWORD=exporter-pw",
		"MAX_CONNECTIONS=100",
		`hostssl all all 0.0.0.0/0 scram-sha-256`,
		`hostssl replication all 0.0.0.0/0 scram-sha-256`,
	} {
		if !strings.Contains(userdata, want) {
			t.Errorf("userdata missing %q", want)
		}
	}

	// TLS material is written as base64-encoded write_files entries — decode
	// and check they round-trip to the exact PEM values.
	for _, pem := range []string{"CA-CERT-PEM", "SERVER-CERT-PEM", "SERVER-KEY-PEM"} {
		encoded := base64.StdEncoding.EncodeToString([]byte(pem))
		if !strings.Contains(userdata, encoded) {
			t.Errorf("userdata missing base64 of %q", pem)
		}
	}
	// The CA private key must never reach the guest.
	caKeyEncoded := base64.StdEncoding.EncodeToString([]byte("CA-KEY-PEM"))
	if strings.Contains(userdata, caKeyEncoded) {
		t.Error("userdata must not embed the CA private key")
	}
}

func TestBuildCloudInitVMPasswordBlock(t *testing.T) {
	withPw, _ := BuildCloudInit(func() BootstrapParams {
		p := testBootstrapParams()
		p.VMPassword = "console-pw"
		return p
	}(), testMaterial())
	if !strings.Contains(withPw, `password: "console-pw"`) || !strings.Contains(withPw, "ssh_pwauth: true") {
		t.Error("userdata missing VM password block when VMPassword is set")
	}

	withoutPw, _ := BuildCloudInit(testBootstrapParams(), testMaterial())
	if strings.Contains(withoutPw, "ssh_pwauth") {
		t.Error("userdata must omit the VM password block when VMPassword is empty")
	}
}

// A baked image has every catalog-supported PostgreSQL major version's
// binaries pre-installed side by side — bootstrap.sh must drop whatever
// clusters were auto-created at package-install time and create exactly one,
// for the tenant's requested EngineVersion, rather than installing anything
// live over the network (the whole point of baking).
func TestBuildCloudInitActivatesRequestedEngineVersion(t *testing.T) {
	p := testBootstrapParams()
	p.EngineVersion = "17"
	userdata, _ := BuildCloudInit(p, testMaterial())

	for _, want := range []string{
		"ENGINE_VERSION='17'",
		`pg_dropcluster --stop "$ver" main`,
		`pg_createcluster --start "${ENGINE_VERSION}" main`,
		`PG_VER="${ENGINE_VERSION}"`,
	} {
		if !strings.Contains(userdata, want) {
			t.Errorf("userdata missing %q", want)
		}
	}
}

// Regression guard: the cluster-reset block must never run
// once /var/lib/postgresql is already the mounted, persistent pgdata disk —
// dropping "main" there would delete live tenant data. Only reset the
// throwaway clusters apt created on a still-OS-disk-backed path.
func TestBuildCloudInitGuardsClusterResetAgainstMountedPgdata(t *testing.T) {
	userdata, _ := BuildCloudInit(testBootstrapParams(), testMaterial())

	if !strings.Contains(userdata, "if ! findmnt -n /var/lib/postgresql") {
		t.Error("userdata missing the findmnt guard before the cluster-reset block")
	}
	// The guard must wrap the destructive drop/create pair, not just precede
	// it — assert drop appears strictly after the guard opens.
	guardIdx := strings.Index(userdata, "if ! findmnt -n /var/lib/postgresql")
	dropIdx := strings.Index(userdata, "pg_dropcluster")
	if guardIdx == -1 || dropIdx == -1 || dropIdx < guardIdx {
		t.Fatalf("pg_dropcluster must appear after the findmnt guard, got guardIdx=%d dropIdx=%d", guardIdx, dropIdx)
	}
}

// TestUserDataTouchesTheProbedBootstrapMarker pins the contract between the
// script that writes the readiness marker and the probe that tests for it.
// The two live in different packages (the path constant has to sit in
// harvester, since credentials imports it and not the reverse), so nothing
// but this test stops the string in the bash below from drifting away from
// harvester.GuestBootstrapCompleteMarker. If it does, the probe waits on a
// file nobody ever creates and every instance stays permanently not-Ready.
func TestUserDataTouchesTheProbedBootstrapMarker(t *testing.T) {
	userdata, _ := BuildCloudInit(testBootstrapParams(), testMaterial())

	touch := "touch " + harvester.GuestBootstrapCompleteMarker
	if !strings.Contains(userdata, touch) {
		t.Fatalf("userdata never runs %q — the readiness probe would block forever", touch)
	}

	// Ordering is the whole point: the marker means "the master role and its
	// database exist", so it must be touched strictly after the bootstrap
	// SQL. Touched before, it re-opens the exact race it was added to close —
	// pg_isready passes, phase goes available, and the first client login
	// fails with "password authentication failed" against a role PostgreSQL
	// has not created yet.
	sqlIdx := strings.Index(userdata, "CREATE ROLE \"${MASTER_USER}\"")
	touchIdx := strings.Index(userdata, touch)
	if sqlIdx == -1 {
		t.Fatal("userdata missing the master-role CREATE ROLE statement")
	}
	if touchIdx < sqlIdx {
		t.Fatalf("marker touched at %d, before the master-role SQL at %d — readiness would go true too early", touchIdx, sqlIdx)
	}

	// ...and strictly before the exporter block, so a monitoring failure
	// under `set -e` cannot hold back DatabaseReady. Monitoring is a separate
	// axis (DerivePhaseSummary reports it as degraded-but-available).
	if exporterIdx := strings.Index(userdata, "/etc/default/prometheus-postgres-exporter"); exporterIdx != -1 && touchIdx > exporterIdx {
		t.Fatalf("marker touched at %d, after exporter setup at %d — monitoring failures would block database readiness", touchIdx, exporterIdx)
	}
}

func TestBuildCloudInitBackupConfig(t *testing.T) {
	p := testBootstrapParams()
	p.BackupEnabled = true
	p.S3Config = &dbaasv1.S3BackupConfig{Endpoint: "s3.example.com", Bucket: "backups", Region: "us-east-1", SecretRef: "s3-creds"}
	userdata, _ := BuildCloudInit(p, testMaterial())

	for _, want := range []string{"S3_ENDPOINT='s3.example.com'", "S3_BUCKET='backups'", "S3_REGION='us-east-1'", "S3_SECRET_REF='s3-creds'"} {
		if !strings.Contains(userdata, want) {
			t.Errorf("userdata missing %q", want)
		}
	}

	disabled, _ := BuildCloudInit(testBootstrapParams(), testMaterial())
	if !strings.Contains(disabled, "# backups disabled") {
		t.Error("userdata should note backups disabled when BackupEnabled is false")
	}
}

func TestBuildCloudInitBackupConfigNeutralizesShellMetacharacters(t *testing.T) {
	p := testBootstrapParams()
	p.BackupEnabled = true
	p.S3Config = &dbaasv1.S3BackupConfig{
		Endpoint:  "s3.example.com$(touch /tmp/pwned)",
		Bucket:    "backups`touch /tmp/pwned2`",
		Region:    "us-east-1; touch /tmp/pwned3",
		SecretRef: "s3-creds' && touch /tmp/pwned4 && echo '",
	}
	userdata, _ := BuildCloudInit(p, testMaterial())

	for _, want := range []string{
		`S3_ENDPOINT='s3.example.com$(touch /tmp/pwned)'`,
		"S3_BUCKET='backups`touch /tmp/pwned2`'",
		`S3_REGION='us-east-1; touch /tmp/pwned3'`,
		`S3_SECRET_REF='s3-creds'\'' && touch /tmp/pwned4 && echo '\'''`,
	} {
		if !strings.Contains(userdata, want) {
			t.Errorf("userdata missing safely-quoted %q\ngot: %s", want, userdata)
		}
	}
}

func TestBuildCloudInitNetworkDataDHCPDefault(t *testing.T) {
	_, networkdata := BuildCloudInit(testBootstrapParams(), testMaterial())
	if !strings.Contains(networkdata, "dhcp4: true") {
		t.Errorf("networkdata = %q, want dhcp4: true (default)", networkdata)
	}
}

func TestBuildCloudInitNetworkDataStatic(t *testing.T) {
	p := testBootstrapParams()
	p.StaticNetwork = &dbaasv1.NetworkConfig{
		Address:       "192.168.40.50/24",
		Gateway:       "192.168.40.1",
		Nameservers:   []string{"8.8.8.8", "8.8.4.4"},
		SearchDomains: []string{"example.com"},
	}
	_, networkdata := BuildCloudInit(p, testMaterial())

	for _, want := range []string{
		"dhcp4: false",
		"addresses: [192.168.40.50/24]",
		"via: 192.168.40.1",
		`addresses: ["8.8.8.8", "8.8.4.4"]`,
		`search: ["example.com"]`,
	} {
		if !strings.Contains(networkdata, want) {
			t.Errorf("networkdata missing %q, got: %s", want, networkdata)
		}
	}
}
