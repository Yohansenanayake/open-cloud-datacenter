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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// newResizeFixture: the VM in the cluster is shaped db.t3.small/20Gi; the test
// varies the *spec* (class/storage) to create drift, plus the VM's runStrategy
// and observed readiness to walk the cold-resize sequence.
func newResizeFixture(t *testing.T, class string, storageGB int, rs kubevirtv1.VirtualMachineRunStrategy, readiness harvester.VMIReadiness) (*DBInstanceReconciler, *dbaasv1.DBInstance, *stubHarvester) {
	t.Helper()
	inst := newProvisionInst()
	inst.Spec.DBInstanceClass = class
	inst.Spec.AllocatedStorage = storageGB
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Resources.DataVolumeName = "pg-orders-data"
	stub := &stubHarvester{readiness: readiness}
	vm := shapedVM("pg-orders", "tenant-a", "db.t3.small", 20, "pg-orders-data", rs)
	r := newProvisionReconciler(t, stub, inst, vm)
	return r, inst, stub
}

func TestEnsureStorageResizeNoDriftSatisfied(t *testing.T) {
	r, inst, stub := newResizeFixture(t, "db.t3.small", 20, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	setStepCond(inst, dbaasv1.ConditionStorageChangeRejected, metav1.ConditionTrue,
		dbaasv1.ReasonUnsupportedShrink, "previous rejection")

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionStorageReady) {
		t.Fatal("StorageReady should be True with no drift")
	}
	if inst.Status.GetCondition(dbaasv1.ConditionStorageChangeRejected) != nil {
		t.Fatal("StorageChangeRejected should be absent once the request is supported")
	}
	if inst.Status.GetCondition(dbaasv1.ConditionResizeInProgress) != nil {
		t.Fatal("ResizeInProgress should be absent when no resize was active")
	}
	if stub.StopVMCalls+stub.ResizeVMCalls+stub.ResizeDVCalls != 0 {
		t.Fatal("no provider calls expected with no drift")
	}
}

func TestEnsureStorageResizeKeepsActivityUntilDatabaseRecovers(t *testing.T) {
	r, inst, _ := newResizeFixture(t, "db.t3.small", 20, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	setStepCond(inst, dbaasv1.ConditionResizeInProgress, metav1.ConditionTrue,
		dbaasv1.ReasonResizeApplied, "resize applied")
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse,
		dbaasv1.ReasonVMBooting, "VM booting")

	if res := r.ensureResize(context.Background(), inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied after shape convergence", res)
	}
	r.finalizeStatus(inst)
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionResizeInProgress) {
		t.Fatal("ResizeInProgress must remain True while the database is recovering")
	}
	if inst.Status.Phase != dbaasv1.StatusModifying {
		t.Fatalf("phase = %q, want %q while database is recovering", inst.Status.Phase, dbaasv1.StatusModifying)
	}

	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue,
		dbaasv1.ReasonPostgresReady, "PostgreSQL is ready")
	setStepCond(inst, dbaasv1.ConditionMonitoringReady, metav1.ConditionTrue,
		dbaasv1.ReasonMonitoringDeployed, "monitoring ready")
	r.finalizeStatus(inst)
	if inst.Status.GetCondition(dbaasv1.ConditionResizeInProgress) != nil {
		t.Fatal("ResizeInProgress must be removed after shape and database readiness converge")
	}
	if inst.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("phase = %q, want %q after resize completion", inst.Status.Phase, dbaasv1.StatusAvailable)
	}
}

// Class drift on a running VM: first pass requests the halt.
func TestEnsureStorageResizeClassDriftStopsVM(t *testing.T) {
	r, inst, stub := newResizeFixture(t, "db.t3.medium", 20, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "ResizeStopping" {
		t.Fatalf("res = %+v, want Pending/ResizeStopping", res)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVMCalls = %d, want 1", stub.StopVMCalls)
	}
	if stub.ResizeVMCalls != 0 {
		t.Fatal("must not resize while the VM may still be running")
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusModifying {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusModifying)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must go False once the VM is halted for resize")
	}
}

// Halt requested but the VMI is still tearing down: wait, no repeat StopVM.
func TestEnsureStorageResizeWaitsForTeardown(t *testing.T) {
	r, inst, stub := newResizeFixture(t, "db.t3.medium", 20, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{Running: true})

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "ResizeWaitingForTeardown" {
		t.Fatalf("res = %+v, want Pending/ResizeWaitingForTeardown", res)
	}
	if stub.StopVMCalls != 0 || stub.ResizeVMCalls != 0 {
		t.Fatalf("no provider calls during teardown wait, got stop:%d resize:%d", stub.StopVMCalls, stub.ResizeVMCalls)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must stay False while waiting for teardown")
	}
}

// VM down: apply exactly the drifted elements (class here, not storage).
func TestEnsureStorageResizeAppliesClassWhenDown(t *testing.T) {
	r, inst, stub := newResizeFixture(t, "db.t3.medium", 20, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "ResizeApplied" {
		t.Fatalf("res = %+v, want Pending/ResizeApplied", res)
	}
	if stub.ResizeVMCalls != 1 {
		t.Fatalf("ResizeVMCalls = %d, want 1", stub.ResizeVMCalls)
	}
	if stub.ResizeDVCalls != 0 {
		t.Fatalf("ResizeDVCalls = %d, want 0 (storage unchanged)", stub.ResizeDVCalls)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must stay False while the VM is down for resize")
	}
}

// Storage-only grow: only the DataVolume resize is applied.
func TestEnsureStorageResizeAppliesStorageGrow(t *testing.T) {
	r, inst, stub := newResizeFixture(t, "db.t3.small", 50, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "ResizeApplied" {
		t.Fatalf("res = %+v, want Pending/ResizeApplied", res)
	}
	if stub.ResizeDVCalls != 1 {
		t.Fatalf("ResizeDVCalls = %d, want 1", stub.ResizeDVCalls)
	}
	if stub.ResizeVMCalls != 0 {
		t.Fatalf("ResizeVMCalls = %d, want 0 (class unchanged)", stub.ResizeVMCalls)
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusModifying {
		t.Fatalf("phase = %q, want %q for storage growth", inst.Status.Phase, dbaasv1.StatusModifying)
	}
}

func TestEnsureStorageResizeAppliesClassAndStorageTogether(t *testing.T) {
	r, inst, stub := newResizeFixture(t, "db.t3.medium", 50, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "ResizeApplied" {
		t.Fatalf("res = %+v, want Pending/ResizeApplied", res)
	}
	if stub.ResizeVMCalls != 1 || stub.ResizeDVCalls != 1 {
		t.Fatalf("resize calls = VM:%d storage:%d, want 1 each", stub.ResizeVMCalls, stub.ResizeDVCalls)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionResizeInProgress) {
		t.Fatal("ResizeInProgress must be True for a combined class and storage resize")
	}
}

// Shrink can never converge (provider resize is grow-only): fail loudly.
func TestEnsureStorageResizeShrinkIsTerminal(t *testing.T) {
	r, inst, stub := newResizeFixture(t, "db.t3.small", 10, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomeTerminal || res.Reason != "UnsupportedShrink" {
		t.Fatalf("res = %+v, want Terminal/UnsupportedShrink", res)
	}
	if stub.StopVMCalls != 0 {
		t.Fatal("must not halt the VM for a change that can never be applied")
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionStorageChangeRejected)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != string(dbaasv1.ReasonUnsupportedShrink) {
		t.Fatalf("StorageChangeRejected = %+v, want True/UnsupportedShrink", cond)
	}
	if inst.Status.GetCondition(dbaasv1.ConditionDatabaseReady) != nil {
		t.Fatal("DatabaseReady must be untouched — the VM was never halted for a rejected shrink")
	}
}

// A VM without observable shape (no limits, no annotation) is not fought.
func TestEnsureStorageResizeMissingShapeSatisfied(t *testing.T) {
	inst := newProvisionInst()
	inst.Spec.DBInstanceClass = "db.t3.medium" // would drift if the shape were observable
	inst.Status.Resources.VMName = "pg-orders"
	stub := &stubHarvester{}
	rs := kubevirtv1.RunStrategyAlways
	bare := &kubevirtv1.VirtualMachine{}
	bare.Name, bare.Namespace = "pg-orders", "tenant-a"
	bare.Spec.RunStrategy = &rs
	bare.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{}
	r := newProvisionReconciler(t, stub, inst, bare)

	res := r.ensureResize(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied (unobservable shape is skipped)", res)
	}
	if stub.StopVMCalls != 0 {
		t.Fatal("must not touch a VM whose shape cannot be observed")
	}
}
