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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
)

func TestEnsureVMCreatesWhenAbsent(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst)
	convergeCredentials(t, ctx, r, inst)

	res := r.ensureVM(ctx, inst)

	if res.Outcome != OutcomePending || res.Reason != "VMCreated" {
		t.Fatalf("res = %+v, want Pending/VMCreated", res)
	}
	if res.Result.RequeueAfter != 0 {
		t.Fatalf("VM create must be event-driven Pending, got RequeueAfter %v", res.Result.RequeueAfter)
	}
	if stub.CreateVMCalls != 1 {
		t.Fatalf("CreateVMCalls = %d, want 1", stub.CreateVMCalls)
	}

	// ensureVM itself only owns VMName/DataVolumeName/CloudInitSecretName;
	// AdminCredentialsSecretName is ensureCredentials's job (not exercised
	// here since we call ensureVM directly).
	refs := inst.Status.Resources
	if refs.VMName != "pg-orders" || refs.CloudInitSecretName != "pg-orders-cloudinit" || refs.DataVolumeName != "pg-orders-data" {
		t.Fatalf("Resources = %+v, want deterministic pg-orders names", refs)
	}

	a := inst.Status.AppliedSpec
	if a == nil {
		t.Fatal("AppliedSpec not snapshotted")
	}
	if a.MasterUsername != defaultMasterUser || a.OSImage != defaultOSImage ||
		a.Port != defaultPort || a.StorageType != defaultStorageType ||
		a.DBName != "orders" || a.NetworkRef != "tenant-a/data-net" {
		t.Fatalf("AppliedSpec = %+v, want defaulted snapshot", a)
	}

	cond := inst.Status.GetCondition(dbaasv1.ConditionVMReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "VMCreated" {
		t.Fatalf("VMReady = %+v, want False/VMCreated", cond)
	}
	if cond.ObservedGeneration != inst.Generation {
		t.Fatalf("cond ObservedGeneration = %d, want %d", cond.ObservedGeneration, inst.Generation)
	}

	// PR7: the provider is asked to stamp a controller owner reference on the
	// VM it creates.
	if stub.LastVMCreateParams == nil || stub.LastVMCreateParams.Owner == nil {
		t.Fatal("VMCreateParams.Owner not set")
	}
	owner := stub.LastVMCreateParams.Owner
	if owner.Kind != "DBInstance" || owner.Name != "orders" || owner.Controller == nil || !*owner.Controller {
		t.Fatalf("Owner = %+v, want controller ref to the DBInstance", owner)
	}
	if stub.LastVMCreateParams.CloudInitSecretName != "pg-orders-cloudinit" {
		t.Fatalf("CloudInitSecretName = %q, want pg-orders-cloudinit", stub.LastVMCreateParams.CloudInitSecretName)
	}

	// PR8: the cloud-init Secret must exist, owner-ref'd, with rendered content
	// built from the durable Material observed by ensureVM.
	var ci corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "pg-orders-cloudinit"}, &ci); err != nil {
		t.Fatalf("cloud-init secret missing: %v", err)
	}
	if len(ci.Data["userdata"]) == 0 || len(ci.Data["networkdata"]) == 0 {
		t.Fatalf("cloud-init secret has empty userdata/networkdata: %+v", ci.Data)
	}
	if refs := ci.GetOwnerReferences(); len(refs) != 1 || refs[0].Kind != "DBInstance" || refs[0].Controller == nil || !*refs[0].Controller {
		t.Fatalf("cloud-init secret owner refs = %+v, want controller-owned", refs)
	}

	// The tenant credentials Secret created by ensureCredentials is reused.
	var cred corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: credentials.TenantCredentialsSecretName(inst)}, &cred); err != nil {
		t.Fatalf("tenant credentials secret missing: %v", err)
	}
	if cred.StringData["admin_password"] == "" {
		t.Fatal("tenant credentials secret has no admin_password")
	}
}

// VMPassword/StaticNetwork are immutable but had no AppliedSpec coverage at
// all before PR9 — assert they're snapshotted, and that StaticNetwork is a
// defensive copy (not aliasing the spec's pointer).
func TestEnsureVMSnapshotsVMPasswordAndStaticNetwork(t *testing.T) {
	inst := newProvisionInst()
	inst.Spec.VMPassword = "s3cr3t"
	inst.Spec.StaticNetwork = &dbaasv1.NetworkConfig{
		Address: "192.168.40.50/24", Gateway: "192.168.40.1", Nameservers: []string{"1.1.1.1"},
	}
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst)
	convergeCredentials(t, context.Background(), r, inst)

	r.ensureVM(context.Background(), inst)

	a := inst.Status.AppliedSpec
	if a == nil {
		t.Fatal("AppliedSpec not snapshotted")
	}
	if a.VMPassword != "s3cr3t" {
		t.Fatalf("AppliedSpec.VMPassword = %q, want s3cr3t", a.VMPassword)
	}
	if a.StaticNetwork == nil || a.StaticNetwork.Address != "192.168.40.50/24" {
		t.Fatalf("AppliedSpec.StaticNetwork = %+v, want copy of spec.StaticNetwork", a.StaticNetwork)
	}
	if a.StaticNetwork == inst.Spec.StaticNetwork {
		t.Fatal("AppliedSpec.StaticNetwork aliases spec.StaticNetwork, want a defensive copy")
	}
}

func TestEnsureVMSatisfiedWhenPresent(t *testing.T) {
	inst := newProvisionInst()
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst, testVM("pg-orders", "tenant-a"))

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if stub.CreateVMCalls != 0 {
		t.Fatalf("CreateVMCalls = %d, want 0", stub.CreateVMCalls)
	}
	if inst.Status.Resources.VMName != "pg-orders" {
		t.Fatalf("VMName not re-recorded from observation: %q", inst.Status.Resources.VMName)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionVMReady) {
		t.Fatal("VMReady should be True when VM exists")
	}
}

// Out-of-band `kubectl delete vm`: the ref says the VM was created, but
// observation says it is gone — the step must repair, not trust status.
func TestEnsureVMSelfHealsAfterOutOfBandDelete(t *testing.T) {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders" // status remembers a VM that no longer exists
	stub := &stubHarvester{}
	r := newProvisionReconciler(t, stub, inst) // no VM object in the cluster
	convergeCredentials(t, context.Background(), r, inst)

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "VMCreated" {
		t.Fatalf("res = %+v, want Pending/VMCreated (re-create)", res)
	}
	if stub.CreateVMCalls != 1 {
		t.Fatalf("CreateVMCalls = %d, want 1 (self-heal re-create)", stub.CreateVMCalls)
	}
}

func TestEnsureVMCreateErrorIsTransientAndRecordsRefs(t *testing.T) {
	inst := newProvisionInst()
	stub := &stubHarvester{createVMErr: errors.New("harvester unavailable")}
	r := newProvisionReconciler(t, stub, inst)
	convergeCredentials(t, context.Background(), r, inst)

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomeTransient || res.Err == nil {
		t.Fatalf("res = %+v, want Transient with error", res)
	}
	// The VM ref and the already-applied cloud-init Secret ref must be
	// recorded even on failure so TeardownAll can clean up partials.
	refs := inst.Status.Resources
	if refs.VMName == "" || refs.CloudInitSecretName == "" {
		t.Fatalf("Resources = %+v, want refs recorded despite create error", refs)
	}
}
