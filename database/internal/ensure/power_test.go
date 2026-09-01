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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// newPowerFixture builds a harness + instance around a shaped VM with the
// given runStrategy and observed VMI Readiness.
func newPowerFixture(t *testing.T, running bool, rs kubevirtv1.VirtualMachineRunStrategy, Readiness harvester.VMIReadiness) (*testHarness, *dbaasv1.DBInstance, *stubHarvester) {
	t.Helper()
	inst := newProvisionInst()
	inst.Spec.Running = &running
	inst.Status.Resources.VMName = "pg-orders"
	inst.Status.Resources.DataVolumeName = "pg-orders-ordersui-data"
	stub := &stubHarvester{Readiness: Readiness}
	vm := shapedVM("pg-orders", "tenant-a", "db.t3.small", 20, "pg-orders-ordersui-data", rs)
	r := newTestHarness(t, stub, inst, vm)
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

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending", res)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVMCalls = %d, want 1", stub.StartVMCalls)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonStarting) {
		t.Fatalf("PowerStateReady = %+v, want False/Starting", cond)
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

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending", res)
	}
	if res.ControllerResult.RequeueAfter != powerRequeue {
		t.Fatalf("RequeueAfter = %v, want %v", res.ControllerResult.RequeueAfter, powerRequeue)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVMCalls = %d, want 0 while VMI still running", stub.StartVMCalls)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonStartWaitingForTeardown) {
		t.Fatalf("PowerStateReady = %+v, want False/StartWaitingForTeardown", cond)
	}
}

// Declared already running but the VMI hasn't appeared yet: boot in progress,
// nothing to write.
func TestEnsurePowerStateWaitsForBoot(t *testing.T) {
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{})

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending (boot wait)", res)
	}
	if res.ControllerResult.RequeueAfter == 0 {
		t.Fatal("boot wait must carry a timer fallback")
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVMCalls = %d, want 0 (declared layer already correct)", stub.StartVMCalls)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonStarting) {
		t.Fatalf("PowerStateReady = %+v, want False/Starting", cond)
	}
}

func TestEnsurePowerStateStopsRunningVM(t *testing.T) {
	r, inst, stub := newPowerFixture(t, false, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{Running: true})
	// Was Available immediately before the stop request — DatabaseReady starts True.
	inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, "PostgresReady", "ready")

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending", res)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVMCalls = %d, want 1", stub.StopVMCalls)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonStopping) {
		t.Fatalf("PowerStateReady = %+v, want False/Stopping", cond)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must go False immediately when a stop is requested, not wait for health to run")
	}
}

// Once runStrategy is observed Halted, StopVM is NOT re-requested — the step
// waits for the VMI to disappear.
func TestEnsurePowerStateStopWaitsForTeardownWithoutRepeatStop(t *testing.T) {
	r, inst, stub := newPowerFixture(t, false, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{Running: true})
	inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, "PostgresReady", "ready")

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomePending {
		t.Fatalf("res = %+v, want Pending (teardown wait)", res)
	}
	if stub.StopVMCalls != 0 {
		t.Fatalf("StopVMCalls = %d, want 0 (declared layer already Halted)", stub.StopVMCalls)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != string(dbaasv1.ReasonStopping) {
		t.Fatalf("PowerStateReady = %+v, want False/Stopping", cond)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		t.Fatal("DatabaseReady must stay False while waiting for VMI teardown")
	}
}

func TestEnsurePowerStateStoppedSatisfiedAndClearsDegraded(t *testing.T) {
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachineinstances"}, "pg-orders")
	r, inst, stub := newPowerFixture(t, false, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	stub.ReadinessErr = notFound // VMI object fully gone
	inst.SetCurrentCondition(dbaasv1.ConditionDegraded, metav1.ConditionTrue, "PostgresUnreachable", "stale from running state")

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
	inst.SetCurrentCondition(dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue, "CrashLoopDetected", "3 restarts in 10m")

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

func TestEnsurePowerStateRestoresCrashLoopHaltFromVMMarker(t *testing.T) {
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyHalted, harvester.VMIReadiness{})
	var vm kubevirtv1.VirtualMachine
	key := client.ObjectKey{Namespace: inst.Namespace, Name: "pg-orders"}
	if err := r.Get(context.Background(), key, &vm); err != nil {
		t.Fatalf("get VM: %v", err)
	}
	vm.Annotations[dbaasv1.AnnotationCrashLoopHaltedVMIUID] = "vmi-halted"
	if err := r.Update(context.Background(), &vm); err != nil {
		t.Fatalf("mark VM crash-loop halted: %v", err)
	}

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVMCalls = %d, want 0", stub.StartVMCalls)
	}
	if inst.Status.LastKnownVMIUID != "vmi-halted" {
		t.Fatalf("LastKnownVMIUID = %q, want vmi-halted", inst.Status.LastKnownVMIUID)
	}
	halted := inst.Status.GetCondition(dbaasv1.ConditionCrashLoopHalted)
	if halted == nil || halted.Status != metav1.ConditionTrue || halted.Reason != string(dbaasv1.ReasonCrashLoopDetected) {
		t.Fatalf("CrashLoopHalted = %+v, want True/CrashLoopDetected", halted)
	}
	power := inst.Status.GetCondition(dbaasv1.ConditionPowerStateReady)
	if power == nil || power.Status != metav1.ConditionFalse || power.Reason != string(dbaasv1.ReasonCrashLoopHalted) {
		t.Fatalf("PowerStateReady = %+v, want False/CrashLoopHalted", power)
	}
	database := inst.Status.GetCondition(dbaasv1.ConditionDatabaseReady)
	if database == nil || database.Status != metav1.ConditionFalse || database.Reason != string(dbaasv1.ReasonCrashLoopDetected) {
		t.Fatalf("DatabaseReady = %+v, want False/CrashLoopDetected", database)
	}
}

func TestEnsurePowerStateRestoresMarkerForRunningRecoveryVM(t *testing.T) {
	Readiness := harvester.VMIReadiness{
		Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-recovered",
	}
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyAlways, Readiness)
	var vm kubevirtv1.VirtualMachine
	key := client.ObjectKey{Namespace: inst.Namespace, Name: "pg-orders"}
	if err := r.Get(context.Background(), key, &vm); err != nil {
		t.Fatalf("get VM: %v", err)
	}
	vm.Annotations[dbaasv1.AnnotationCrashLoopHaltedVMIUID] = "vmi-halted"
	if err := r.Update(context.Background(), &vm); err != nil {
		t.Fatalf("mark VM crash-loop halted: %v", err)
	}

	if res := r.ensurePowerState(context.Background(), inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("power result = %+v, want Satisfied", res)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted was not restored from the VM marker")
	}
	r.Recorder = record.NewFakeRecorder(10)
	if res := r.ensureDatabaseHealth(context.Background(), inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("health result = %+v, want Satisfied recovery", res)
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted still set after the different VMI recovered")
	}
	if stub.ClearCrashLoopHaltCalls != 1 {
		t.Fatalf("ClearCrashLoopHalt calls = %d, want 1", stub.ClearCrashLoopHaltCalls)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVMCalls = %d, want 0 during out-of-band recovery", stub.StartVMCalls)
	}
}

func TestEnsurePowerStateGenericReadinessErrorIsTransient(t *testing.T) {
	boom := errors.New("apiserver timeout")
	r, inst, stub := newPowerFixture(t, true, kubevirtv1.RunStrategyAlways, harvester.VMIReadiness{})
	stub.ReadinessErr = boom

	res := r.ensurePowerState(context.Background(), inst)

	if res.Outcome != OutcomeTransient || !errors.Is(res.Err, boom) {
		t.Fatalf("res = %+v, want Transient carrying the error", res)
	}
}
