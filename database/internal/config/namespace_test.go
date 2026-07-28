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

func TestPodNamespace(t *testing.T) {
	t.Setenv(podNamespaceEnvironment, "dbaas-system-v2")

	got, err := PodNamespace()
	if err != nil {
		t.Fatalf("PodNamespace() error = %v", err)
	}
	if got != "dbaas-system-v2" {
		t.Fatalf("PodNamespace() = %q, want dbaas-system-v2", got)
	}
}

func TestPodNamespaceRequiresDownwardAPIValue(t *testing.T) {
	t.Setenv(podNamespaceEnvironment, "")

	_, err := PodNamespace()
	if err == nil || !strings.Contains(err.Error(), podNamespaceEnvironment) {
		t.Fatalf("PodNamespace() error = %v, want missing environment error", err)
	}
}

func TestPodNamespaceRejectsInvalidValue(t *testing.T) {
	t.Setenv(podNamespaceEnvironment, "DBAAS_SYSTEM")

	_, err := PodNamespace()
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("PodNamespace() error = %v, want invalid namespace error", err)
	}
}
