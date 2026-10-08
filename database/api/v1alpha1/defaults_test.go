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

import "testing"

func TestDefaultDBName(t *testing.T) {
	for name, want := range map[string]string{
		"orders":            "orders",
		"orders-db":         "orders_db",
		"rt-src-1791115069": "rt_src_1791115069",
		"a.b.c":             "a_b_c",
		"9lives":            "db_9lives",
		"postgres":          "db_postgres",
		"template1":         "db_template1",
		"x123456789-123456789-123456789-123456789-123456789-123456789-123456789": "x123456789_123456789_123456789_123456789_123456789_123456789_12",
	} {
		if got := DefaultDBName(name); got != want {
			t.Errorf("DefaultDBName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEffectiveDBNamePrefersSpec(t *testing.T) {
	inst := &DBInstance{}
	inst.Name = "orders-db"
	if got := inst.EffectiveDBName(); got != "orders_db" {
		t.Fatalf("EffectiveDBName() = %q, want the default orders_db", got)
	}
	inst.Spec.DBName = "appdb"
	if got := inst.EffectiveDBName(); got != "appdb" {
		t.Fatalf("EffectiveDBName() = %q, want spec.dbName appdb", got)
	}
}
