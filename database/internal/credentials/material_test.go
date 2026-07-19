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
	"strings"
	"testing"
)

func TestRenewServerCertRejectsMalformedCAPEM(t *testing.T) {
	bundle, err := generateTLS("db1")
	if err != nil {
		t.Fatalf("generate TLS: %v", err)
	}

	tests := []struct {
		name    string
		caCert  string
		caKey   string
		wantErr string
	}{
		{"invalid CA key", bundle.CACertPEM, "not PEM", "invalid CA key PEM"},
		{"invalid CA certificate", "not PEM", bundle.CAKeyPEM, "invalid CA certificate PEM"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := RenewServerCert(tc.caCert, tc.caKey, "db1", nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
