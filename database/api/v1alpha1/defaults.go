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

import "strings"

// Defaults that are part of the API contract: fixed by the API itself and
// documented on the fields they default, so every client of this package
// resolves them the same way. Defaults chosen by the operator (from its
// config or image catalog) live in internal/ensure instead — this package
// must not depend on either.

// WantRunning reports the desired power state. An omitted running field
// defaults to true.
func (s *DBInstanceSpec) WantRunning() bool {
	return s.Running == nil || *s.Running
}

// EffectiveDBName is the database this instance creates: spec.dbName, or
// DefaultDBName of the instance name when unset.
func (in *DBInstance) EffectiveDBName() string {
	if in.Spec.DBName != "" {
		return in.Spec.DBName
	}
	return DefaultDBName(in.Name)
}

// DefaultDBName turns an instance name into a database name that satisfies
// spec.dbName's own validation, so a default can always be written back into
// a spec (restore does exactly that, and a defaulting webhook would too):
// lowercase, every character outside [a-z0-9_] becomes "_", a leading digit
// gets a "db_" prefix, and the result is capped at 63 characters.
func DefaultDBName(instanceName string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(instanceName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "db_" + name
	}
	if len(name) > 63 {
		name = name[:63]
	}
	switch name {
	case "postgres", "template0", "template1":
		name = "db_" + name
	}
	return name
}
