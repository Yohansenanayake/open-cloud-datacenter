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
