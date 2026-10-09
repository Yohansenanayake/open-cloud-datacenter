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

package v1alpha1

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The longest accepted name must yield a valid metrics Service name, and
// the CRDs (whose markers can only hold literals) must enforce exactly
// MaxInstanceNameLength.
func TestMaxInstanceNameLengthMatchesTheCRDs(t *testing.T) {
	if got := len("pg-" + strings.Repeat("x", MaxInstanceNameLength) + "-metrics"); got != 63 {
		t.Fatalf("metrics Service name for the longest instance name is %d characters, want exactly 63", got)
	}
	limit := strconv.Itoa(MaxInstanceNameLength)
	for file, want := range map[string]string{
		"../../config/crd/bases/dbaas.opencloud.wso2.com_dbinstances.yaml": "size(self.metadata.name) <= " + limit,
		"../../config/crd/bases/dbaas.opencloud.wso2.com_dbrestores.yaml":  "maxLength: " + limit,
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s does not contain %q — the CRD and MaxInstanceNameLength disagree", file, want)
		}
	}
}
