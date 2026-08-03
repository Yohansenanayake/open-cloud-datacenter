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

package ensure

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/catalog"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
)

func TestEffectiveEngineVersionExplicitSupported(t *testing.T) {
	entry := catalog.BakedImageEntry{SupportedEngineVersions: []string{"16", "17"}}
	version, ok := effectiveEngineVersion("16", entry)
	if !ok || version != "16" {
		t.Fatalf("effectiveEngineVersion(16) = (%q, %v), want (16, true)", version, ok)
	}
}

func TestEffectiveEngineVersionExplicitUnsupported(t *testing.T) {
	entry := catalog.BakedImageEntry{SupportedEngineVersions: []string{"16", "17"}}
	if _, ok := effectiveEngineVersion("15", entry); ok {
		t.Fatal("effectiveEngineVersion(15) = ok, want not ok — 15 is not in SupportedEngineVersions")
	}
}

// Unset defaults to the image's highest supported version — the runtime
// bootstrap needs a concrete, non-empty version to create a cluster with,
// and engineVersion was never enforced before the catalog existed, so a
// hard reject here would break every instance that never set it.
func TestEffectiveEngineVersionUnsetDefaultsToHighest(t *testing.T) {
	entry := catalog.BakedImageEntry{SupportedEngineVersions: []string{"16", "17"}}
	version, ok := effectiveEngineVersion("", entry)
	if !ok || version != "17" {
		t.Fatalf("effectiveEngineVersion(\"\") = (%q, %v), want (17, true)", version, ok)
	}
}

// A catalog entry with no supported versions at all is a data-integrity bug
// (every real BakedImageEntry should list at least one) — there's nothing
// to default to, so this must fail rather than pass an empty string through
// to bootstrap.sh's pg_createcluster call.
func TestEffectiveEngineVersionNoSupportedVersionsIsNotOK(t *testing.T) {
	if _, ok := effectiveEngineVersion("", catalog.BakedImageEntry{}); ok {
		t.Fatal("effectiveEngineVersion with no SupportedEngineVersions = ok, want not ok")
	}
}

func TestImmutableDriftNormalizesCreateDefaults(t *testing.T) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders"},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.medium",
			AllocatedStorage: 50,
			NetworkRef:       "default/vm-network",
			DBName:           "orders",
			MasterUsername:   "dbadmin",
			Port:             5432,
			StorageType:      "longhorn",
		},
		Status: dbaasv1.DBInstanceStatus{
			AppliedSpec: &dbaasv1.AppliedSpec{
				NetworkRef: "default/vm-network",
			},
		},
	}

	if drift := immutableDriftWithDefaults(inst, operatorconfig.Default().DatabaseDefaults); drift != "" {
		t.Fatalf("immutableDrift() = %q, want no drift", drift)
	}
}

func TestImmutableDriftDetectsActualImmutableChange(t *testing.T) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders"},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.medium",
			AllocatedStorage: 50,
			NetworkRef:       "default/vm-network",
			DBName:           "orders-v2",
		},
		Status: dbaasv1.DBInstanceStatus{
			AppliedSpec: &dbaasv1.AppliedSpec{
				NetworkRef:     "default/vm-network",
				DBName:         "orders",
				MasterUsername: "dbadmin",
				Port:           5432,
				StorageType:    "longhorn",
			},
		},
	}

	if drift := immutableDriftWithDefaults(inst, operatorconfig.Default().DatabaseDefaults); drift != "dbName" {
		t.Fatalf("immutableDrift() = %q, want dbName", drift)
	}
}

func TestImmutableDriftDetectsVMPasswordChange(t *testing.T) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders"},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.medium",
			AllocatedStorage: 50,
			NetworkRef:       "default/vm-network",
			VMPassword:       "changed",
		},
		Status: dbaasv1.DBInstanceStatus{
			AppliedSpec: &dbaasv1.AppliedSpec{
				NetworkRef: "default/vm-network",
				VMPassword: "original",
			},
		},
	}

	if drift := immutableDriftWithDefaults(inst, operatorconfig.Default().DatabaseDefaults); drift != "vmPassword" {
		t.Fatalf("immutableDrift() = %q, want vmPassword", drift)
	}
}

func TestImmutableDriftDetectsStaticNetworkChange(t *testing.T) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders"},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.medium",
			AllocatedStorage: 50,
			NetworkRef:       "default/vm-network",
			StaticNetwork: &dbaasv1.NetworkConfig{
				Address: "192.168.40.51/24", Gateway: "192.168.40.1", Nameservers: []string{"1.1.1.1"},
			},
		},
		Status: dbaasv1.DBInstanceStatus{
			AppliedSpec: &dbaasv1.AppliedSpec{
				NetworkRef: "default/vm-network",
				StaticNetwork: &dbaasv1.NetworkConfig{
					Address: "192.168.40.50/24", Gateway: "192.168.40.1", Nameservers: []string{"1.1.1.1"},
				},
			},
		},
	}

	if drift := immutableDriftWithDefaults(inst, operatorconfig.Default().DatabaseDefaults); drift != "staticNetwork" {
		t.Fatalf("immutableDrift() = %q, want staticNetwork", drift)
	}
}

// A distinct pointer with an identical value must not be treated as drift —
// guards against a naive pointer-identity comparison creeping back in.
func TestImmutableDriftStaticNetworkSameValueDifferentPointerIsNotDrift(t *testing.T) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders"},
		Spec: dbaasv1.DBInstanceSpec{
			DBInstanceClass:  "db.t3.medium",
			AllocatedStorage: 50,
			NetworkRef:       "default/vm-network",
			StaticNetwork: &dbaasv1.NetworkConfig{
				Address: "192.168.40.50/24", Gateway: "192.168.40.1", Nameservers: []string{"1.1.1.1"},
			},
		},
		Status: dbaasv1.DBInstanceStatus{
			AppliedSpec: &dbaasv1.AppliedSpec{
				NetworkRef: "default/vm-network",
				StaticNetwork: &dbaasv1.NetworkConfig{
					Address: "192.168.40.50/24", Gateway: "192.168.40.1", Nameservers: []string{"1.1.1.1"},
				},
			},
		},
	}

	if drift := immutableDriftWithDefaults(inst, operatorconfig.Default().DatabaseDefaults); drift != "" {
		t.Fatalf("immutableDrift() = %q, want no drift", drift)
	}
}
