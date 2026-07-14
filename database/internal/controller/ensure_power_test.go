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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// newPowerFixture builds a reconciler + instance around a shaped VM with the
// given runStrategy and observed VMI readiness.
func newPowerFixture(t *testing.T, running bool, rs kubevirtv1.VirtualMachineRunStrategy, readiness harvester.VMIReadiness) (*DBInstanceReconciler, *dbaasv1.DBInstance, *stubHarvester) {
	t.Helper()
	inst := newProvisionInst()
	inst.Spec.Running = &running
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Resources.DataVolumeName = "pg-orders-data"
	stub := &stubHarvester{readiness: readiness}
	vm := shapedVM("pg-orders", "tenant-a", "db.t3.small", 20, "pg-orders-data", rs)
	r := newProvisionReconciler(t, stub, inst, vm)
	return r, inst, stub
}

func TestEnsurePowerStateRunningSatisfied(t *testing.T) {
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionPowerStateReady) {
		t.Fatal("PowerStateReady should be True")
	}
	if stub.StartVMCalls+stub.StopVMCalls != 0 {
		t.Fatalf("no provider calls expected, got start=%d stop=%d", stub.StartVMCalls, stub.StopVMCalls)
	}
}

// A halted VM whose spec wants running (planned start, or an out-of-band halt)
// is started once the previous VMI is fully gone.
func TestEnsurePowerStateStartsHaltedVM(t *testing.T) {
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	inst.Status.LastKnownVMIUID = "old-uid"
	inst.Status.ObservedGeneration = inst.Generation - 1

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "Starting" {
		t.Fatalf("res = %+v, want Pending/Starting", res)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVMCalls = %d, want 1", stub.StartVMCalls)
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStarting)
	}
	if inst.Status.LastKnownVMIUID != "" {
		t.Fatal("LastKnownVMIUID must be cleared on a planned start")
	}
}

// The KubeVirt start subresource rejects while a VMI object exists: no StartVM
// until the previous VMI finishes tearing down.
func TestEnsurePowerStateStartWaitsForTeardown(t *testing.T) {
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{Running: true})

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "StartWaitingForTeardown" {
		t.Fatalf("res = %+v, want Pending/StartWaitingForTeardown", res)
	}
	if res.Result.RequeueAfter != powerRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.Result.RequeueAfter, powerRequeue)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVMCalls = %d, want 0 while VMI still running", stub.StartVMCalls)
	}
}

// Declared already running but the VMI hasn't appeared yet: boot in progress,
// nothing to write.
func TestEnsurePowerStateWaitsForBoot(t *testing.T) {
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{})

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "Starting" {
		t.Fatalf("res = %+v, want Pending/Starting (boot wait)", res)
	}
	if res.Result.RequeueAfter == 0 {
		t.Fatal("boot wait must carry a timer fallback")
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVMCalls = %d, want 0 (declared layer already correct)", stub.StartVMCalls)
	}
}

func TestEnsurePowerStateStopsRunningVM(t *testing.T) {
	r, inst, stub := newPowerFixture(t, false, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	// Was Available immediately before the stop request — DatabaseReady starts True.
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, "PostgresReady", "ready")

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "Stopping" {
		t.Fatalf("res = %+v, want Pending/Stopping", res)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVMCalls = %d, want 1", stub.StopVMCalls)
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusStopping {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStopping)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must go False immediately when a stop is requested, not wait for health to run")
	}
}

// Once runStrategy is observed Halted, StopVM is NOT re-requested — the step
// waits for the VMI to disappear.
func TestEnsurePowerStateStopWaitsForTeardownWithoutRepeatStop(t *testing.T) {
	r, inst, stub := newPowerFixture(t, false, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{Running: true})
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, "PostgresReady", "ready")

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending || res.Reason != "Stopping" {
		t.Fatalf("res = %+v, want Pending/Stopping (teardown wait)", res)
	}
	if stub.StopVMCalls != 0 {
		t.Fatalf("StopVMCalls = %d, want 0 (declared layer already Halted)", stub.StopVMCalls)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must stay False while waiting for VMI teardown")
	}
}

func TestEnsurePowerStateStoppedSatisfiedAndClearsDegraded(t *testing.T) {
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachineinstances"}, "pg-orders")
	r, inst, stub := newPowerFixture(t, false, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	stub.readinessErr = notFound // VMI object fully gone
	setStepCond(inst, dbaasv1.ConditionDegraded, metav1.ConditionTrue, "PostgresUnreachable", "stale from running state")

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "Stopped" {
		t.Fatalf("PowerStateReady = %+v, want True/Stopped", cond)
	}
	if inst.Status.GetCondition(dbaasv1.ConditionDegraded) != nil {
		t.Fatal("stale Degraded condition must be cleared when stopped")
	}
}

// CrashLoopHalted suspends power management entirely: no StartVM (spec.running
// must not resurrect a crash-looper) and no StopVM (health halted it at
// detection; an out-of-band recovery start must not be fought). Satisfied so the
// pass reaches health, which owns park/recovery.
func TestEnsurePowerStateHonoursCrashLoopHalt(t *testing.T) {
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	setStepCond(inst, dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue, "CrashLoopDetected", "3 restarts in 10m")

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied (power suspended, pass continues to health)", res)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("calls = stop:%d start:%d, want none while crash-loop halted", stub.StopVMCalls, stub.StartVMCalls)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "CrashLoopHalted" {
		t.Fatalf("PowerStateReady = %+v, want False/CrashLoopHalted", cond)
	}
}

func TestEnsurePowerStateGenericReadinessErrorIsTransient(t *testing.T) {
	boom := errors.New("apiserver timeout")
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{})
	stub.readinessErr = boom

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomeTransient || !errors.Is(res.Err, boom) {
		t.Fatalf("res = %+v, want Transient carrying the error", res)
	}
}
