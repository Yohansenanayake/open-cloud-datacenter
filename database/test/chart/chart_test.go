package chart_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// Keep the Helm install path in sync with the controller's generated APIs and
// permissions, including tenant helper roles for snapshots and restores.
func TestChartMatchesManifests(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart rendering")
	}
	for _, enabled := range []bool{true, false} {
		name := "enabled"
		if !enabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			args := []string{"template", "merge-check", "../../charts/chart", "--namespace", "chart-test"}
			if enabled {
				args = append(args, "--set", "rbac.helpers.enable=true")
			} else {
				args = append(args, "--set", "crd.enable=false")
			}
			data, err := exec.Command("helm", args...).Output()
			if err != nil {
				t.Fatalf("helm template: %v", err)
			}
			objects := map[string]map[string]interface{}{}
			decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
			for {
				var object map[string]interface{}
				if err := decoder.Decode(&object); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if object == nil {
					continue
				}
				meta := object["metadata"].(map[string]interface{})
				objects[object["kind"].(string)+"/"+meta["name"].(string)] = object
			}
			paths, err := filepath.Glob("../../config/crd/bases/*.yaml")
			if err != nil || len(paths) == 0 {
				t.Fatalf("find CRDs: %v", err)
			}
			paths = append(paths, "../../config/rbac/role.yaml")
			helpers, err := filepath.Glob("../../config/rbac/db*_role.yaml")
			if err != nil {
				t.Fatal(err)
			}
			paths = append(paths, helpers...)
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var expected map[string]interface{}
				if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096).Decode(&expected); err != nil {
					t.Fatal(err)
				}
				meta := expected["metadata"].(map[string]interface{})
				kind := expected["kind"].(string)
				name := meta["name"].(string)
				field := "spec"
				if kind == "ClusterRole" {
					field = "rules"
					name = "merge-check-dbaas-operator-" + name
				}
				actual, found := objects[kind+"/"+name]
				want := enabled || filepath.Base(path) == "role.yaml"
				if found != want {
					t.Errorf("%s/%s present=%v, want %v", kind, name, found, want)
					continue
				}
				if !want {
					continue
				}
				if !reflect.DeepEqual(expected[field], actual[field]) {
					t.Errorf("%s/%s %s differs from %s", kind, name, field, path)
				}
				actualMeta := actual["metadata"].(map[string]interface{})
				if kind == "CustomResourceDefinition" {
					annotations, _ := actualMeta["annotations"].(map[string]interface{})
					if annotations["helm.sh/resource-policy"] != "keep" {
						t.Errorf("%s must be retained on uninstall", name)
					}
				} else if labels, ok := meta["labels"].(map[string]interface{}); ok {
					actualLabels, _ := actualMeta["labels"].(map[string]interface{})
					for key, value := range labels {
						if strings.HasPrefix(key, "rbac.authorization.k8s.io/") && actualLabels[key] != value {
							t.Errorf("%s is missing aggregation label %s", name, key)
						}
					}
				}
			}
		})
	}
}
