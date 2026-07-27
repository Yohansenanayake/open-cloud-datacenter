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

// Full-reconcile crash-loop parking and recovery (PR6): condition-driven via
// CrashLoopHalted. The health step detects and halts at the threshold; the
// power step refuses to start while halted; recovery is an out-of-band
// operator start observed healthy.

import (
	"context"
	"testing"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

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
	stub.Readiness = harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-uid-recovered"}

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
