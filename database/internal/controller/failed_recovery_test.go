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

// Crash-loop detection, park, and recovery (PR6): condition-driven via
// CrashLoopHalted. ensureDatabaseHealth detects (and halts at the threshold);
// ensurePowerState refuses to start while halted; recovery is an out-of-band
// operator start observed healthy.

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

// KI-006 Problem A regression: under RunStrategyAlways KubeVirt auto-recovers a
// crash-looping VM forever. A chain of crashLoopThreshold unplanned restarts
// (UID changes), each within crashLoopWindow, must halt the VM once and park the
// instance under CrashLoopHalted + phase=failed.
func TestCrashLoopHaltsAtThreshold(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newCaughtUpFixture(stub)
	ctx := context.Background()

	for i := 1; i <= crashLoopThreshold; i++ {
		stub.readiness.VMIUID = fmt.Sprintf("vmi-uid-crash-%d", i)
		res := r.ensureDatabaseHealth(ctx, inst)

		if i < crashLoopThreshold {
			if res.Outcome != OutcomeSatisfied {
				t.Fatalf("cycle %d: res = %+v, want Satisfied (absorbed)", i, res)
			}
			if inst.Status.Phase == dbaasv1.StatusFailed {
				t.Fatalf("cycle %d: failed before threshold", i)
			}
			if inst.Status.RecentUnplannedRestarts != i {
				t.Fatalf("cycle %d: RecentUnplannedRestarts = %d, want %d", i, inst.Status.RecentUnplannedRestarts, i)
			}
			continue
		}

		// Threshold cycle: halt + park.
		if res.Outcome != OutcomePending {
			t.Fatalf("threshold cycle: res = %+v, want Pending", res)
		}
		if res.Result.RequeueAfter != crashLoopParkRequeue {
			t.Fatalf("RequeueAfter = %v, want %v (cold park probe)", res.Result.RequeueAfter, crashLoopParkRequeue)
		}
	}

	if stub.StopVMForCrashLoopCalls != 1 {
		t.Fatalf("StopVMForCrashLoop called %d times, want 1 (halt exactly once, at detection)", stub.StopVMForCrashLoopCalls)
	}
	if stub.StopVMCalls != 0 {
		t.Fatalf("plain StopVM called %d times, want 0", stub.StopVMCalls)
	}
	if stub.LastHaltedVMIUID != "vmi-uid-crash-3" {
		t.Fatalf("halted VMI UID = %q, want vmi-uid-crash-3", stub.LastHaltedVMIUID)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVM called %d times, want 0", stub.StartVMCalls)
	}
	crashLoopHalted := inst.Status.GetCondition(dbaasv1.ConditionCrashLoopHalted)
	if crashLoopHalted == nil || crashLoopHalted.Status != metav1.ConditionTrue ||
		crashLoopHalted.Reason != string(dbaasv1.ReasonCrashLoopDetected) {
		t.Fatalf("CrashLoopHalted = %+v, want True/CrashLoopDetected", crashLoopHalted)
	}
	r.finalizeStatus(inst)
	if inst.Status.Phase != dbaasv1.StatusCrashLoopHalted {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusCrashLoopHalted)
	}
}

// Window decay: an unplanned restart after more than crashLoopWindow of quiet
// starts a fresh chain instead of extending the old one.
func TestCrashLoopChainResetsAfterQuietGap(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-new"},
	}
	r, inst := newCaughtUpFixture(stub)
	stale := metav1.NewTime(time.Now().Add(-crashLoopWindow - time.Minute))
	inst.Status.RecentUnplannedRestarts = crashLoopThreshold - 1
	inst.Status.LastUnplannedRestartTime = &stale

	res := r.ensureDatabaseHealth(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("res = %+v, want Satisfied", res)
	}
	if inst.Status.RecentUnplannedRestarts != 1 {
		t.Fatalf("RecentUnplannedRestarts = %d, want 1 (chain must reset after quiet gap)", inst.Status.RecentUnplannedRestarts)
	}
	if inst.Status.Phase == dbaasv1.StatusFailed {
		t.Fatal("instance failed despite the chain being broken by a quiet gap")
	}
	if stub.StopVMCalls != 0 {
		t.Fatalf("StopVM called %d times, want 0", stub.StopVMCalls)
	}
}

// seedCrashLoopPark puts a full-Reconcile fixture into the parked state: halted
// VM, CrashLoopHalted condition, phase=failed, generation observed.
func seedCrashLoopPark(t *testing.T, r *DBInstanceReconciler) {
	t.Helper()
	ctx := context.Background()
	inst := getInst(t, r.Client)
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.Phase = dbaasv1.StatusFailed
	inst.Status.Message = "seeded crash-loop park"
	inst.Status.Conditions = []metav1.Condition{
		{
			Type:               dbaasv1.ConditionCrashLoopHalted,
			Status:             metav1.ConditionTrue,
			Reason:             string(dbaasv1.ReasonCrashLoopDetected),
			Message:            "seeded",
			LastTransitionTime: metav1.Now(),
		},
		{
			Type:               dbaasv1.ConditionMonitoringReady,
			Status:             metav1.ConditionTrue,
			Reason:             string(dbaasv1.ReasonMonitoringDeployed),
			Message:            "monitoring resources observed",
			ObservedGeneration: inst.Generation,
			LastTransitionTime: metav1.Now(),
		},
	}
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed: %v", err)
	}
	setVMRunStrategy(t, r.Client, "pg-orders", "tenant-a", kubevirtv1.RunStrategyHalted)
}

// KI-007 regression, PR6 shape: a parked instance idles cold — 30s re-probes,
// no VM calls, no phase drift — instead of hot-looping or resurrecting.
func TestCrashLoopParkDoesNotHotLoop(t *testing.T) {
	stub := &stubHarvester{} // VMI gone (halted)
	r, req := newLifecycleFixture(t, true, stub)
	seedCrashLoopPark(t, r)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		res, err := r.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("reconcile %d error: %v", i, err)
		}
		if res.RequeueAfter != crashLoopParkRequeue {
			t.Fatalf("reconcile %d: RequeueAfter = %v, want %v", i, res.RequeueAfter, crashLoopParkRequeue)
		}
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM calls while parked (stop=%d start=%d), want none — spec.running=true must not resurrect", stub.StopVMCalls, stub.StartVMCalls)
	}
	inst := getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusCrashLoopHalted {
		t.Fatalf("parked instance drifted to phase %q, want crash-loop-halted", inst.Status.Phase)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted lost while parked")
	}
}

// Intentional recovery path: an operator repairs the guest and starts the VM
// out-of-band; once observed fully healthy the instance clears CrashLoopHalted
// and converges back to Available in the same reconcile — with no controller
// start/stop calls (power must not fight the operator's start).
func TestCrashLoopRecoversWhenHealthyOutOfBand(t *testing.T) {
	stub := &stubHarvester{}
	r, req := newLifecycleFixture(t, true, stub)
	seedCrashLoopPark(t, r)
	ctx := context.Background()
	inst := getInst(t, r.Client)
	inst.Status.LastKnownVMIUID = "vmi-uid-crashed"
	inst.Status.RecentUnplannedRestarts = crashLoopThreshold
	inst.Status.RestartCount = crashLoopThreshold
	now := metav1.Now()
	inst.Status.LastUnplannedRestartTime = &now
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed restart history: %v", err)
	}

	// Operator starts the VM out-of-band and it comes up healthy.
	setVMRunStrategy(t, r.Client, "pg-orders", "tenant-a", kubevirtv1.RunStrategyAlways)
	stub.readiness = harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-uid-recovered"}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("recovery reconcile error: %v", err)
	}

	inst = getInst(t, r.Client)
	if inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		t.Fatal("CrashLoopHalted still set after healthy out-of-band recovery")
	}
	if inst.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("Phase = %q, want %q after recovery", inst.Status.Phase, dbaasv1.StatusAvailable)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready should be True after recovery converges")
	}
	if inst.Status.LastKnownVMIUID != "vmi-uid-recovered" {
		t.Fatalf("LastKnownVMIUID = %q, want re-baselined to the recovered VMI", inst.Status.LastKnownVMIUID)
	}
	if inst.Status.RecentUnplannedRestarts != 0 {
		t.Fatalf("RecentUnplannedRestarts = %d, want reset after recovery", inst.Status.RecentUnplannedRestarts)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("controller VM calls during out-of-band recovery (stop=%d start=%d), want none", stub.StopVMCalls, stub.StartVMCalls)
	}
	if stub.ClearCrashLoopHaltCalls != 1 {
		t.Fatalf("ClearCrashLoopHalt calls = %d, want 1", stub.ClearCrashLoopHaltCalls)
	}
}
