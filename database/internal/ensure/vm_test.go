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
	"errors"
	"strings"
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
	r := newTestHarness(t, stub, inst)
	convergeCredentials(t, ctx, r, inst)

	res := r.ensureVM(ctx, inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending", res)
	}
	if res.ControllerResult.RequeueAfter != 0 {
		t.Fatalf("VM create must be event-driven Pending, got RequeueAfter %v", res.ControllerResult.RequeueAfter)
	}
	if stub.CreateVMCalls != 1 {
		t.Fatalf("CreateVMCalls = %d, want 1", stub.CreateVMCalls)
	}

	// ensureVM itself only owns VMName/DataVolumeName/CloudInitSecretName;
	// AdminCredentialsSecretName is ensureCredentials's job (not exercised
	// here since we call ensureVM directly).
	refs := inst.Status.Resources
	if refs.VMName != "pg-orders" || refs.CloudInitSecretName != "pg-orders-cloudinit" || refs.DataVolumeName != "pg-orders-ordersui-data" {
		t.Fatalf("Resources = %+v, want deterministic pg-orders names", refs)
	}

	a := inst.Status.AppliedSpec
	if a == nil {
		t.Fatal("AppliedSpec not snapshotted")
	}
	if a.MasterUsername != defaultMasterUser ||
		a.Port != defaultPort || a.StorageType != defaultStorageType ||
		a.DBName != "orders" || a.NetworkRef != "tenant-a/data-net" {
		t.Fatalf("AppliedSpec = %+v, want defaulted snapshot", a)
	}
	// CurrentImageRevision/OSDiskPVCName live outside AppliedSpec — they're
	// expected to change on every repave, unlike everything checked above.
	if inst.Status.CurrentImageRevision != defaultBakedImageName {
		t.Fatalf("CurrentImageRevision = %q, want %q", inst.Status.CurrentImageRevision, defaultBakedImageName)
	}
	if inst.Status.Resources.OSDiskPVCName != "pg-orders-ordersui-os" {
		t.Fatalf("OSDiskPVCName = %q, want pg-orders-ordersui-os", inst.Status.Resources.OSDiskPVCName)
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
	// inst.Spec.EngineVersion is unset — effectiveEngineVersion must default
	// to defaultBakedImageName's configured DefaultEngineVersion ("17")
	// rather than leaving bootstrap.sh's ENGINE_VERSION empty.
	if !strings.Contains(string(ci.Data["userdata"]), "ENGINE_VERSION='17'") {
		t.Fatalf("cloud-init userdata missing defaulted ENGINE_VERSION='17': %s", ci.Data["userdata"])
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
	r := newTestHarness(t, stub, inst)
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
	r := newTestHarness(t, stub, inst, testVM("pg-orders", "tenant-a"))

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
	if inst.Status.Resources.DataVolumeName != "pg-orders-ordersui-data" {
		t.Fatalf("DataVolumeName not reconstructed: %q", inst.Status.Resources.DataVolumeName)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionVMReady) {
		t.Fatal("VMReady should be True when VM exists")
	}
}

// OSDiskPVCName can't be recomputed by formula once a repave has happened
// (it becomes revision-suffixed), unlike VMName/DataVolumeName — so if
// status was lost/reset while the VM keeps running, this field must be
// self-healed from live observation the same way its two siblings are.
func TestEnsureVMSelfHealsOSDiskPVCNameWhenPresent(t *testing.T) {
	inst := newProvisionInst()
	stub := &stubHarvester{OSDiskPVCName: "pg-orders-ordersui-os-ubuntu-2404-postgres-v20260815"}
	r := newTestHarness(t, stub, inst, testVM("pg-orders", "tenant-a"))

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Resources.OSDiskPVCName != stub.OSDiskPVCName {
		t.Fatalf("OSDiskPVCName not self-healed from observation: got %q, want %q",
			inst.Status.Resources.OSDiskPVCName, stub.OSDiskPVCName)
	}
}

// A transient Harvester read failure while self-healing OSDiskPVCName must
// not report Satisfied — the field could be silently left stale/empty.
func TestEnsureVMSelfHealOSDiskPVCNameErrorIsTransient(t *testing.T) {
	inst := newProvisionInst()
	boom := errors.New("boom")
	stub := &stubHarvester{OSDiskPVCNameErr: boom}
	r := newTestHarness(t, stub, inst, testVM("pg-orders", "tenant-a"))

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomeTransient {
		t.Fatalf("Outcome = %q, want Transient", res.Outcome)
	}
}

// Out-of-band `kubectl delete vm`: the ref says the VM was created, but
// observation says it is gone — the step must repair, not trust status.
func TestEnsureVMSelfHealsAfterOutOfBandDelete(t *testing.T) {
	inst := newProvisionInst()
	inst.Status.Resources.VMName = "pg-orders" // status remembers a VM that no longer exists
	stub := &stubHarvester{}
	r := newTestHarness(t, stub, inst) // no VM object in the cluster
	convergeCredentials(t, context.Background(), r, inst)

	res := r.ensureVM(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending (re-create)", res)
	}
	if stub.CreateVMCalls != 1 {
		t.Fatalf("CreateVMCalls = %d, want 1 (self-heal re-create)", stub.CreateVMCalls)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionVMReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonVMCreated) {
		t.Fatalf("VMReady = %+v, want False/VMCreated", cond)
	}
}

// Regression guard for T004: a deleted-and-recreated DBInstance (same name,
// new UID — Kubernetes never reuses UIDs) must get disk names disjoint from
// its previous incarnation, so a leaked old disk (TeardownAll not deleting
// PVCs is a separate, still-open issue) can never be silently reattached
// under a fresh Secret with a different password.
func TestEnsureVMDiskNamesDisjointAcrossSameNameRecreate(t *testing.T) {
	first := newProvisionInst()
	first.UID = "11111111-1111-1111-1111-111111111111"
	stub1 := &stubHarvester{}
	r1 := newTestHarness(t, stub1, first)
	convergeCredentials(t, context.Background(), r1, first)
	if res := r1.ensureVM(context.Background(), first); res.Outcome != OutcomePending {
		t.Fatalf("first res = %+v, want Pending", res)
	}

	second := newProvisionInst() // same Name/Namespace, fresh Status — simulates delete+recreate
	second.UID = "22222222-2222-2222-2222-222222222222"
	stub2 := &stubHarvester{}
	r2 := newTestHarness(t, stub2, second)
	convergeCredentials(t, context.Background(), r2, second)
	if res := r2.ensureVM(context.Background(), second); res.Outcome != OutcomePending {
		t.Fatalf("second res = %+v, want Pending", res)
	}

	if first.Status.Resources.DataVolumeName == second.Status.Resources.DataVolumeName {
		t.Fatalf("DataVolumeName collided across recreate: both %q", first.Status.Resources.DataVolumeName)
	}
	if first.Status.Resources.OSDiskPVCName == second.Status.Resources.OSDiskPVCName {
		t.Fatalf("OSDiskPVCName collided across recreate: both %q", first.Status.Resources.OSDiskPVCName)
	}
	if second.Status.Resources.DataVolumeName != "pg-orders-22222222-data" {
		t.Fatalf("DataVolumeName = %q, want pg-orders-22222222-data", second.Status.Resources.DataVolumeName)
	}
	if second.Status.Resources.OSDiskPVCName != "pg-orders-22222222-os" {
		t.Fatalf("OSDiskPVCName = %q, want pg-orders-22222222-os", second.Status.Resources.OSDiskPVCName)
	}
}

func TestEnsureVMCreateErrorIsTransientAndRecordsRefs(t *testing.T) {
	inst := newProvisionInst()
	stub := &stubHarvester{CreateVMErr: errors.New("harvester unavailable")}
	r := newTestHarness(t, stub, inst)
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
