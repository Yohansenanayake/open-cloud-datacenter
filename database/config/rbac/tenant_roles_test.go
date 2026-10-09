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

// Package rbac tests tenant ClusterRoles aggregated into the built-in
// admin, edit, and view roles for all DBaaS resource kinds.
package rbac

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

type crd struct {
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Kind   string `json:"kind"`
			Plural string `json:"plural"`
		} `json:"names"`
	} `json:"spec"`
}

func loadCRDs(t *testing.T) []crd {
	t.Helper()
	paths, err := filepath.Glob("../crd/bases/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no CRDs found under ../crd/bases (err %v)", err)
	}
	var out []crd
	for _, p := range paths {
		var c crd
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if err := yaml.Unmarshal(data, &c); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		out = append(out, c)
	}
	return out
}

// loadRole reads <kind>_<role>_role.yaml and checks kustomization.yaml
// lists it, so a role file that's written but never deployed fails too.
func loadRole(t *testing.T, file string) *rbacv1.ClusterRole {
	t.Helper()
	kustomization, err := os.ReadFile("kustomization.yaml")
	if err != nil {
		t.Fatalf("read kustomization.yaml: %v", err)
	}
	if !strings.Contains(string(kustomization), "- "+file+"\n") {
		t.Errorf("%s is not listed in kustomization.yaml, so it is never deployed", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("missing tenant role %s: %v", file, err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(data, &role); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return &role
}

func verbsFor(role *rbacv1.ClusterRole, group, resource string) []string {
	var verbs []string
	for _, rule := range role.Rules {
		if slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) {
			verbs = append(verbs, rule.Verbs...)
		}
	}
	slices.Sort(verbs)
	return slices.Compact(verbs)
}

// Every CRD gets an admin, editor and viewer role aggregated into the
// built-in role of the same level, with the expected verbs. Status is
// written by the operator alone, so tenants only ever read it.
func TestEveryCRDHasAggregatedTenantRoles(t *testing.T) {
	levels := []struct {
		role, aggregateLabel string
		verbs                []string
	}{
		{"admin", "rbac.authorization.k8s.io/aggregate-to-admin", []string{"*"}},
		{"editor", "rbac.authorization.k8s.io/aggregate-to-edit", []string{"create", "delete", "get", "list", "patch", "update", "watch"}},
		{"viewer", "rbac.authorization.k8s.io/aggregate-to-view", []string{"get", "list", "watch"}},
	}
	for _, c := range loadCRDs(t) {
		kind := strings.ToLower(c.Spec.Names.Kind)
		for _, level := range levels {
			t.Run(kind+"/"+level.role, func(t *testing.T) {
				role := loadRole(t, kind+"_"+level.role+"_role.yaml")
				if role.Labels[level.aggregateLabel] != "true" {
					t.Errorf("%s is not aggregated (%s), so tenants never receive it", role.Name, level.aggregateLabel)
				}
				if got := verbsFor(role, c.Spec.Group, c.Spec.Names.Plural); !slices.Equal(got, level.verbs) {
					t.Errorf("%s verbs on %s = %v, want %v", role.Name, c.Spec.Names.Plural, got, level.verbs)
				}
				if got := verbsFor(role, c.Spec.Group, c.Spec.Names.Plural+"/status"); !slices.Equal(got, []string{"get"}) {
					t.Errorf("%s verbs on %s/status = %v, want [get] (only the operator writes status)", role.Name, c.Spec.Names.Plural, got)
				}
			})
		}
	}
}

// Tenant roles never reach the hold and slot Leases that coordinate
// backups, restores and repave, nor anything outside the
// DBaaS API group.
func TestTenantRolesGrantNothingOutsideTheDBaaSGroup(t *testing.T) {
	paths, err := filepath.Glob("*_role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		var role rbacv1.ClusterRole
		data, _ := os.ReadFile(p)
		if err := yaml.Unmarshal(data, &role); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		aggregated := false
		for label := range role.Labels {
			aggregated = aggregated || strings.HasPrefix(label, "rbac.authorization.k8s.io/aggregate-to-")
		}
		if !aggregated {
			continue // not a tenant role (the operator's own, metrics, ...)
		}
		for _, rule := range role.Rules {
			for _, g := range rule.APIGroups {
				if g != "dbaas.opencloud.wso2.com" {
					t.Errorf("tenant role %s (%s) grants API group %q", role.Name, p, g)
				}
			}
		}
	}
}

// Every CRD is registered in kubebuilder's PROJECT file. Nothing at build
// or run time reads it, but kubebuilder tooling does — e.g. a Helm chart
// generated per resource would silently leave out an unregistered kind and
// its tenant roles.
func TestEveryCRDIsRegisteredInPROJECT(t *testing.T) {
	data, err := os.ReadFile("../../PROJECT")
	if err != nil {
		t.Fatalf("read PROJECT: %v", err)
	}
	var project struct {
		Resources []struct {
			Kind string `json:"kind"`
		} `json:"resources"`
	}
	if err := yaml.Unmarshal(data, &project); err != nil {
		t.Fatalf("parse PROJECT: %v", err)
	}
	registered := map[string]bool{}
	for _, r := range project.Resources {
		registered[r.Kind] = true
	}
	for _, c := range loadCRDs(t) {
		if !registered[c.Spec.Names.Kind] {
			t.Errorf("CRD kind %s is not registered in PROJECT", c.Spec.Names.Kind)
		}
	}
}
