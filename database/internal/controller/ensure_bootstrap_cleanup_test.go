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

package controller

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func newCleanupFixture(stub *stubHarvester, dbReady bool) (*DBInstanceReconciler, *dbaasv1.DBInstance) {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Resources.CloudInitSecretName = "pg-orders-cloudinit"
	status := metav1.ConditionFalse
	if dbReady {
		status = metav1.ConditionTrue
	}
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, status, "test", "test")
	return &DBInstanceReconciler{Harvester: stub}, inst
}

func TestEnsureBootstrapCleanupScrubsOnceDBReady(t *testing.T) {
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(stub, true)

	res := r.ensureBootstrapCleanup(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "CloudInitScrubbed" {
		t.Fatalf("res = %+v, want Pending/CloudInitScrubbed", res)
	}
	if inst.Status.Resources.CloudInitSecretName != "" {
		t.Fatalf("CloudInitSecretName = %q, want cleared", inst.Status.Resources.CloudInitSecretName)
	}
	// FailedMount guard: the disk reference must leave the VM spec BEFORE the
	// secret is deleted, or a VMI restart in between mounts a missing secret.
	if len(stub.OpsLog) != 2 || stub.OpsLog[0] != "RemoveCloudInitDisk" || stub.OpsLog[1] != "DeleteSecret" {
		t.Fatalf("OpsLog = %v, want [RemoveCloudInitDisk DeleteSecret]", stub.OpsLog)
	}
}

// Cloud-init is only provably consumed once the in-guest probe has passed:
// scrubbing earlier could break a first boot.
func TestEnsureBootstrapCleanupDefersUntilDBReady(t *testing.T) {
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(stub, false)

	res := r.ensureBootstrapCleanup(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied (deferred)", res)
	}
	if inst.Status.Resources.CloudInitSecretName == "" {
		t.Fatal("ref must be kept while cloud-init may still be needed")
	}
	if len(stub.OpsLog) != 0 {
		t.Fatalf("OpsLog = %v, want no provider calls", stub.OpsLog)
	}
}

func TestEnsureBootstrapCleanupSatisfiedWhenNothingToScrub(t *testing.T) {
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(stub, true)
	inst.Status.Resources.CloudInitSecretName = ""

	if res := r.ensureBootstrapCleanup(context.Background(), inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if len(stub.OpsLog) != 0 {
		t.Fatalf("OpsLog = %v, want no provider calls", stub.OpsLog)
	}
}

// A failed disk removal must keep the ref (retry next pass) and must NOT delete
// the secret — deleting after a failed removal is exactly the FailedMount trap.
func TestEnsureBootstrapCleanupDiskRemovalFailureKeepsSecret(t *testing.T) {
	stub := &stubHarvester{removeCloudInitErr: errors.New("vm update conflict")}
	r, inst := newCleanupFixture(stub, true)

	res := r.ensureBootstrapCleanup(context.Background(), inst)

	if res.Outcome != OutcomeTransient {
		t.Fatalf("res = %+v, want Transient", res)
	}
	if inst.Status.Resources.CloudInitSecretName == "" {
		t.Fatal("ref must survive a failed scrub for the retry")
	}
	for _, op := range stub.OpsLog {
		if op == "DeleteSecret" {
			t.Fatal("secret deleted despite disk removal failure (FailedMount trap)")
		}
	}
}
