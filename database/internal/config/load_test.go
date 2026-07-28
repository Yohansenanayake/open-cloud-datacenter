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
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearConfigurationEnvironment(t)

	got, err := Load(flag.NewFlagSet("test", flag.ContinueOnError), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Default()
	if got.Controller != want.Controller {
		t.Fatalf("Controller = %+v, want %+v", got.Controller, want.Controller)
	}
	if got.Logging != want.Logging {
		t.Fatalf("Logging = %+v, want %+v", got.Logging, want.Logging)
	}
	if len(got.InstanceClasses) != len(want.InstanceClasses) {
		t.Fatalf("InstanceClasses has %d entries, want %d", len(got.InstanceClasses), len(want.InstanceClasses))
	}
}

func TestLoadPrecedence(t *testing.T) {
	clearConfigurationEnvironment(t)
	path := writeConfig(t, `{
		"controller": {"maxConcurrentReconciles": 2},
		"server": {"gateway": {"bindAddress": ":8082"}}
	}`)
	t.Setenv("DBAAS_CONTROLLER__MAX_CONCURRENT_RECONCILES", "3")
	t.Setenv("DBAAS_SERVER__GATEWAY__BIND_ADDRESS", ":8083")

	got, err := load(flag.NewFlagSet("test", flag.ContinueOnError), []string{
		"--controller.maxConcurrentReconciles=4",
	}, path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Controller.MaxConcurrentReconciles != 4 {
		t.Fatalf("MaxConcurrentReconciles = %d, want flag value 4", got.Controller.MaxConcurrentReconciles)
	}
	if got.Server.Gateway.BindAddress != ":8083" {
		t.Fatalf("Gateway.BindAddress = %q, want environment value :8083", got.Server.Gateway.BindAddress)
	}
}

func TestExplicitFlagEqualToDefaultStillWins(t *testing.T) {
	clearConfigurationEnvironment(t)
	path := writeConfig(t, `{"controller": {"maxConcurrentReconciles": 9}}`)

	got, err := load(flag.NewFlagSet("test", flag.ContinueOnError), []string{
		"--controller.maxConcurrentReconciles=1",
	}, path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Controller.MaxConcurrentReconciles != 1 {
		t.Fatalf("MaxConcurrentReconciles = %d, want explicit default value 1", got.Controller.MaxConcurrentReconciles)
	}
}

func TestEnvironmentNameNormalization(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv("DBAAS_OBSERVABILITY__MONITORING__SCRAPE_INTERVAL", "45s")
	t.Setenv("DBAAS_DATABASE_DEFAULTS__MASTER_USERNAME", "platform_admin")

	got, err := Load(flag.NewFlagSet("test", flag.ContinueOnError), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Observability.Monitoring.ScrapeInterval != 45*time.Second {
		t.Fatalf("ScrapeInterval = %v, want 45s", got.Observability.Monitoring.ScrapeInterval)
	}
	if got.DatabaseDefaults.MasterUsername != "platform_admin" {
		t.Fatalf("MasterUsername = %q, want platform_admin", got.DatabaseDefaults.MasterUsername)
	}
}

func TestConfiguredInstanceClassesReplaceBuiltIns(t *testing.T) {
	clearConfigurationEnvironment(t)
	path := writeConfig(t, `{
		"instanceClasses": {
			"db.custom.small": {
				"cpuCores": 2,
				"memoryMB": 3072,
				"maxConnections": 75
			}
		}
	}`)

	got, err := load(flag.NewFlagSet("test", flag.ContinueOnError), nil, path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.InstanceClasses) != 1 {
		t.Fatalf("InstanceClasses = %#v, want configured map to replace built-ins", got.InstanceClasses)
	}
	if class := got.InstanceClasses["db.custom.small"]; class.CPUCores != 2 || class.MemoryMB != 3072 {
		t.Fatalf("custom class = %+v", class)
	}
}

func TestMissingFixedFileIsOptional(t *testing.T) {
	clearConfigurationEnvironment(t)
	missing := filepath.Join(t.TempDir(), "missing.json")

	got, err := load(flag.NewFlagSet("test", flag.ContinueOnError), nil, missing)
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if got.Controller.MaxConcurrentReconciles != Default().Controller.MaxConcurrentReconciles {
		t.Fatalf("MaxConcurrentReconciles = %d, want built-in default", got.Controller.MaxConcurrentReconciles)
	}
}

func TestConfigFileFlagIsNotSupported(t *testing.T) {
	clearConfigurationEnvironment(t)

	_, err := Load(flag.NewFlagSet("test", flag.ContinueOnError), []string{"--config-file=/tmp/config.json"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -config-file") {
		t.Fatalf("Load() error = %v, want unsupported config-file flag error", err)
	}
}

func TestConfigFileEnvironmentVariableDoesNotSelectFile(t *testing.T) {
	clearConfigurationEnvironment(t)
	path := writeConfig(t, `{"controller": {"maxConcurrentReconciles": 9}}`)
	t.Setenv("DBAAS_CONFIG_FILE", path)

	got, err := load(flag.NewFlagSet("test", flag.ContinueOnError), nil,
		filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if got.Controller.MaxConcurrentReconciles != Default().Controller.MaxConcurrentReconciles {
		t.Fatalf("MaxConcurrentReconciles = %d, want fixed-path defaults", got.Controller.MaxConcurrentReconciles)
	}
}

func TestValidationFailureIdentifiesField(t *testing.T) {
	clearConfigurationEnvironment(t)

	_, err := Load(flag.NewFlagSet("test", flag.ContinueOnError),
		[]string{"--controller.maxConcurrentReconciles=0"})
	if err == nil || !strings.Contains(err.Error(), "controller.maxConcurrentReconciles") {
		t.Fatalf("Load() error = %v, want field-specific validation error", err)
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func clearConfigurationEnvironment(t *testing.T) {
	t.Helper()
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, envPrefix) || name == "POD_NAMESPACE" {
			t.Setenv(name, "")
		}
	}
}
