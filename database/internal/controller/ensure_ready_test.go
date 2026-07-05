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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestEnsureReadyStampsAvailableAndObservedGeneration(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Generation = 5

	res := r.ensureReady(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusAvailable)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseAvailable {
		t.Fatalf("ProvisioningPhase = %q, want %q (hand-off to phaseAvailable)", inst.Status.ProvisioningPhase, dbaasv1.PhaseAvailable)
	}
	if inst.Status.ObservedGeneration != 5 {
		t.Fatalf("ObservedGeneration = %d, want 5 — only ensureReady advances it", inst.Status.ObservedGeneration)
	}
	cond := inst.Status.GetCondition(dbaasv1.ConditionReady)
	if cond == nil || cond.ObservedGeneration != 5 {
		t.Fatalf("Ready condition = %+v, want True with ObservedGeneration 5", cond)
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready should be True")
	}
}

// A deliberately stopped instance converges to phase=stopped with Ready=False —
// stopped is a converged state, and Ready is never left stale-True.
func TestEnsureReadyStampsStoppedWhenNotRunning(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Generation = 4
	stopped := false
	inst.Spec.Running = &stopped

	res := r.ensureReady(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Phase != dbaasv1.StatusStopped {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStopped)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseStopped {
		t.Fatalf("ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseStopped)
	}
	if inst.Status.ObservedGeneration != 4 {
		t.Fatalf("ObservedGeneration = %d, want 4 (stop observed)", inst.Status.ObservedGeneration)
	}
	if inst.Status.GetCondition(dbaasv1.ConditionReady) == nil {
		t.Fatal("Ready condition missing")
	}
	if inst.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready must be False on a stopped instance")
	}
}
