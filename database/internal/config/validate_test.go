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

package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigurationIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
}

func TestValidateTLSFiles(t *testing.T) {
	cfg := Default()
	cfg.Observability.Metrics.TLS.CertDir = "/certs"
	cfg.Observability.Metrics.TLS.KeyFile = ""

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "observability.metrics.tls.keyFile") {
		t.Fatalf("Validate() error = %v, want metrics TLS key field", err)
	}
}

func TestValidateBindAddressRejectsNamedPort(t *testing.T) {
	cfg := Default()
	cfg.Server.Gateway.BindAddress = ":http"

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "server.gateway.bindAddress") {
		t.Fatalf("Validate() error = %v, want gateway bind-address field", err)
	}
}

func TestValidateRejectsEmptyOSVersion(t *testing.T) {
	for _, value := range []string{"", "   ", "\t"} {
		cfg := Default()
		cfg.DatabaseDefaults.OSVersion = value

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "databaseDefaults.osVersion") {
			t.Fatalf("Validate() with OSVersion = %q error = %v, want databaseDefaults.osVersion field", value, err)
		}
	}
}

func TestValidateRejectsInvalidImageNamespace(t *testing.T) {
	for _, value := range []string{"", "Default", "my_namespace"} {
		cfg := Default()
		cfg.Infrastructure.Harvester.ImageNamespace = value

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "infrastructure.harvester.imageNamespace") {
			t.Fatalf("Validate() with ImageNamespace = %q error = %v, want infrastructure.harvester.imageNamespace field", value, err)
		}
	}
}

func TestValidateRejectsInvalidBackupSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*Config)
		field  string
	}{
		"maxConcurrent 0":  {func(c *Config) { c.Backup.MaxConcurrent = 0 }, "backup.maxConcurrent"},
		"maxConcurrent -1": {func(c *Config) { c.Backup.MaxConcurrent = -1 }, "backup.maxConcurrent"},
		"timeout 0":        {func(c *Config) { c.Backup.Timeout = 0 }, "backup.timeout"},
		"timeout negative": {func(c *Config) { c.Backup.Timeout = -time.Minute }, "backup.timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Validate() error = %v, want it to name %s", err, tc.field)
			}
		})
	}
}

// Half of the global cap, rounded up, and never 0 — a namespace must
// always be able to run something.
func TestBackupPerNamespaceShare(t *testing.T) {
	for k, want := range map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 3, 8: 4} {
		if got := (BackupConfig{MaxConcurrent: k}).PerNamespace(); got != want {
			t.Errorf("PerNamespace() with MaxConcurrent %d = %d, want %d", k, got, want)
		}
	}
}

func TestValidateRejectsNonPositiveRestoreRecoveryTimeout(t *testing.T) {
	for _, value := range []time.Duration{0, -time.Minute} {
		cfg := Default()
		cfg.Restore.RecoveryTimeout = value

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "restore.recoveryTimeout") {
			t.Fatalf("Validate() with RecoveryTimeout = %v error = %v, want restore.recoveryTimeout field", value, err)
		}
	}
}

func TestValidateRequiresRestoreTimeoutToExceedRecoveryTimeout(t *testing.T) {
	for _, value := range []time.Duration{0, time.Hour, 30 * time.Minute} {
		cfg := Default()
		cfg.Restore.RecoveryTimeout = time.Hour
		cfg.Restore.Timeout = value

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "restore.timeout") {
			t.Fatalf("Validate() with Timeout = %v error = %v, want restore.timeout field", value, err)
		}
	}
}
