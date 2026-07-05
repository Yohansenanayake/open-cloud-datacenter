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
	"fmt"
	"testing"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hasCondition(inst *dbaasv1.DBInstance, condType string) bool {
	for _, c := range inst.Status.Conditions {
		if c.Type == condType {
			return true
		}
	}
	return false
}

// TestFailedStateDoesNotHotLoop is the KI-007 regression: a failed instance
// must idle on 30s requeues — no status writes, no VM calls — instead of
// re-entering phaseAvailable on every status-write event.
func TestFailedStateDoesNotHotLoop(t *testing.T) {
	stub := &stubHarvester{} // VMI unhealthy (zero readiness)
	r, req := newLifecycleFixture(t, true, stub)
	ctx := context.Background()

	inst := getInst(t, r.Client)
	inst.Status.Phase = dbaasv1.StatusFailed
	inst.Status.ProvisioningPhase = dbaasv1.PhaseFailed
	inst.Status.Message = "seeded failed"
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed: %v", err)
	}

	for i := 0; i < 5; i++ {
		res, err := r.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("reconcile %d error: %v", i, err)
		}
		if res.RequeueAfter != 30*time.Second {
			t.Fatalf("reconcile %d: RequeueAfter = %v, want 30s", i, res.RequeueAfter)
		}
	}
	inst = getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusFailed || inst.Status.ProvisioningPhase != dbaasv1.PhaseFailed {
		t.Fatalf("parked instance drifted (phase=%q provisioning=%q), want failed/Failed", inst.Status.Phase, inst.Status.ProvisioningPhase)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM calls while parked in failed (stop=%d start=%d), want none", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// TestPhaseFailedRecoversWhenHealthy verifies the intentional recovery path:
// once the VMI reports fully healthy, the instance clears Failed, resets its
// episode counters, and re-enters the provisioning chain.
func TestPhaseFailedRecoversWhenHealthy(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-uid-new"},
	}
	r, req := newLifecycleFixture(t, true, stub)
	ctx := context.Background()

	inst := getInst(t, r.Client)
	inst.Status.Phase = dbaasv1.StatusFailed
	inst.Status.ProvisioningPhase = dbaasv1.PhaseFailed
	inst.Status.Conditions = []metav1.Condition{{
		Type:               dbaasv1.ConditionFailed,
		Status:             metav1.ConditionTrue,
		Reason:             dbaasv1.ReasonCrashLoopDetected,
		Message:            "seeded",
		LastTransitionTime: metav1.Now(),
	}}
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	inst = getInst(t, r.Client)
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want %q (re-enter provisioning chain)", inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
	}
	if inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStarting)
	}
	if hasCondition(inst, dbaasv1.ConditionFailed) {
		t.Fatalf("ConditionFailed still present after recovery")
	}
}

// TestCrashLoopHaltsAndFailsAtThreshold is the KI-006 Problem A regression:
// under RunStrategyAlways, KubeVirt auto-recovers a crash-looping VM forever
// and the episode guard never fires. A chain of crashLoopThreshold unplanned
// restarts (UID changes), each within crashLoopWindow, must halt the VM and
// set phase=failed + provisioningPhase=Failed.
func TestCrashLoopHaltsAndFailsAtThreshold(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	// Each cycle KubeVirt has recreated the VMI: new UID every pass.
	for i := 1; i <= crashLoopThreshold; i++ {
		stub.readiness.VMIUID = fmt.Sprintf("vmi-uid-crash-%d", i)
		if _, err := r.phaseAvailable(ctx, inst); err != nil {
			t.Fatalf("cycle %d error: %v", i, err)
		}
		if i < crashLoopThreshold {
			if inst.Status.Phase == dbaasv1.StatusFailed {
				t.Fatalf("cycle %d: failed before threshold", i)
			}
			if inst.Status.RecentUnplannedRestarts != i {
				t.Fatalf("cycle %d: RecentUnplannedRestarts = %d, want %d", i, inst.Status.RecentUnplannedRestarts, i)
			}
			if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
				t.Fatalf("cycle %d: ProvisioningPhase = %q, want %q (normal restart absorb)", i, inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
			}
		}
	}

	if inst.Status.Phase != dbaasv1.StatusFailed {
		t.Fatalf("Phase = %q after %d chained unplanned restarts, want %q", inst.Status.Phase, crashLoopThreshold, dbaasv1.StatusFailed)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseFailed {
		t.Fatalf("ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseFailed)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVM called %d times, want 1 (crash-looping VM must be halted, not left to KubeVirt)", stub.StopVMCalls)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVM called %d times, want 0", stub.StartVMCalls)
	}
	if !hasCondition(inst, dbaasv1.ConditionFailed) {
		t.Fatalf("ConditionFailed not set")
	}
}

// TestCrashLoopChainResetsAfterQuietGap verifies the window decay: an
// unplanned restart arriving after more than crashLoopWindow of quiet starts
// a fresh chain instead of extending the old one.
func TestCrashLoopChainResetsAfterQuietGap(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-new"},
	}
	r, inst := newAvailableReconciler(stub)
	// Two chained restarts happened, but the last one was before the window.
	stale := metav1.NewTime(time.Now().Add(-crashLoopWindow - time.Minute))
	inst.Status.RecentUnplannedRestarts = crashLoopThreshold - 1
	inst.Status.LastUnplannedRestartTime = &stale
	ctx := context.Background()

	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.Status.RecentUnplannedRestarts != 1 {
		t.Fatalf("RecentUnplannedRestarts = %d, want 1 (chain must reset after quiet gap)", inst.Status.RecentUnplannedRestarts)
	}
	if inst.Status.Phase == dbaasv1.StatusFailed {
		t.Fatalf("instance failed despite the chain being broken by a quiet gap")
	}
	if stub.StopVMCalls != 0 {
		t.Fatalf("StopVM called %d times, want 0", stub.StopVMCalls)
	}
}
