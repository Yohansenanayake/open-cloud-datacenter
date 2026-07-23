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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestEnsureBootstrapCleanupDefersUntilDatabaseReady(t *testing.T) {
	inst := newProvisionInst()
	inst.Status.Resources.CloudInitSecretName = "pg-orders-cloudinit"
	inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonPostgresInitializing, "initializing")

	res := (&testHarness{}).ensureBootstrapCleanup(context.Background(), inst)
	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
}

func TestEnsureBootstrapCleanupSatisfiedWhenNothingToScrub(t *testing.T) {
	inst := newProvisionInst()
	inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, dbaasv1.ReasonPostgresReady, "ready")

	res := (&testHarness{}).ensureBootstrapCleanup(context.Background(), inst)
	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
}
