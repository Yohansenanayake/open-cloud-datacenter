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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

const cleanupCloudInitSecretName = "pg-orders-cloudinit"

// newCleanupFixture wires a real fake client (with the cloud-init Secret
// pre-created) rather than a bare stub: DeleteSecret moved off
// harvester.ClientInterface (PR9) onto the controller's own client, so these
// tests need something real to delete against. The client is wrapped to keep
// recording "DeleteSecret" into stub.OpsLog alongside the Harvester calls, so
// the FailedMount-ordering assertions below stay unchanged.
func newCleanupFixture(t *testing.T, stub *stubHarvester, dbReady bool) (*DBInstanceReconciler, *dbaasv1.DBInstance) {
	t.Helper()
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Resources.CloudInitSecretName = cleanupCloudInitSecretName
	status := metav1.ConditionFalse
	if dbReady {
		status = metav1.ConditionTrue
	}
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, status, "test", "test")

	ciSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: cleanupCloudInitSecretName, Namespace: inst.Namespace,
	}}
	r := newProvisionReconciler(t, stub, inst, ciSecret)
	wrapClientForSecretDeleteTracking(t, r, stub, cleanupCloudInitSecretName)
	return r, inst
}

func TestEnsureBootstrapCleanupScrubsOnceDBReady(t *testing.T) {
	ctx := context.Background()
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(t, stub, true)

	res := r.ensureBootstrapCleanup(ctx, inst)

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
	var got corev1.Secret
	key := types.NamespacedName{Namespace: inst.Namespace, Name: cleanupCloudInitSecretName}
	if err := r.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("cloud-init secret Get = %v, want NotFound", err)
	}
}

// Cloud-init is only provably consumed once the in-guest probe has passed:
// scrubbing earlier could break a first boot.
func TestEnsureBootstrapCleanupDefersUntilDBReady(t *testing.T) {
	stub := &stubHarvester{}
	r, inst := newCleanupFixture(t, stub, false)

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
	r, inst := newCleanupFixture(t, stub, true)
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
	ctx := context.Background()
	stub := &stubHarvester{removeCloudInitErr: errors.New("vm update conflict")}
	r, inst := newCleanupFixture(t, stub, true)

	res := r.ensureBootstrapCleanup(ctx, inst)

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
	var got corev1.Secret
	key := types.NamespacedName{Namespace: inst.Namespace, Name: cleanupCloudInitSecretName}
	if err := r.Get(ctx, key, &got); err != nil {
		t.Fatalf("cloud-init secret must survive a failed scrub, Get: %v", err)
	}
}
