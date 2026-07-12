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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestEnsureReadyStampsAvailableAndObservedGeneration(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Generation = 5
	// In the real flow ensureDatabaseHealth sets this before ready runs.
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, "PostgresReady", "ready")

	res := r.ensureReady(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusAvailable)
	}
	if inst.Status.ObservedGeneration != 5 {
		t.Fatalf("ObservedGeneration = %d, want 5 — only ensureReady advances it", inst.Status.ObservedGeneration)
	}
}

// A deliberately stopped instance converges to phase=stopped — stopped is a
// converged state, so ObservedGeneration still advances.
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
	if inst.Status.ObservedGeneration != 4 {
		t.Fatalf("ObservedGeneration = %d, want 4 (stop observed)", inst.Status.ObservedGeneration)
	}
}

// A caught-up degraded blip reaches ready as report-only: phase must stay
// "degraded" as health set it — ensureReady must not overwrite it with
// Available just because ObservedGeneration is advancing.
func TestEnsureReadyDoesNotOverwritePhaseWhenDegraded(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Generation = 6
	inst.Status.Phase = dbaasv1.StatusDegraded // set by health's report-only branch
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse,
		"PostgresUnreachable", "probe failing")

	res := r.ensureReady(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Phase != dbaasv1.StatusDegraded {
		t.Fatalf("Phase = %q, want %q (must not overwrite with available)", inst.Status.Phase, dbaasv1.StatusDegraded)
	}
	if inst.Status.ObservedGeneration != 6 {
		t.Fatalf("ObservedGeneration = %d, want 6 (generation converged; degradation is runtime)", inst.Status.ObservedGeneration)
	}
}

// --- syncReadyCondition: recomputed every pass, independent of ensureReady ---

func TestSyncReadyConditionTrueWhenDatabaseReady(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, "PostgresReady", "ready")

	r.syncReadyCondition(inst)

	if !inst.Status.IsConditionTrue(dbaasv1.ConditionReady) {
		t.Fatal("Ready should be True when DatabaseReady is True")
	}
}

func TestSyncReadyConditionFalseWhenStopped(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	stopped := false
	inst.Spec.Running = &stopped
	// Even a stale True DatabaseReady must not leak through: wantRunning wins.
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue, "PostgresReady", "ready")

	r.syncReadyCondition(inst)

	cond := inst.Status.GetCondition(dbaasv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "Stopped" {
		t.Fatalf("Ready = %+v, want False/Stopped", cond)
	}
}

// Ready mirrors DatabaseReady's own reason/message rather than hardcoding one,
// since syncReadyCondition runs regardless of which step last touched
// DatabaseReady (health's degraded report, a crash-loop halt, a resize halt).
func TestSyncReadyConditionMirrorsDatabaseReadyReason(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse,
		"PostgresUnreachable", "probe failing")

	r.syncReadyCondition(inst)

	cond := inst.Status.GetCondition(dbaasv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "PostgresUnreachable" {
		t.Fatalf("Ready = %+v, want False/PostgresUnreachable", cond)
	}
}

// A brand-new instance has no DatabaseReady condition at all yet — must fall
// back to a generic reason rather than nil-dereferencing.
func TestSyncReadyConditionFalseWhenDatabaseReadyNeverSet(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()

	r.syncReadyCondition(inst)

	cond := inst.Status.GetCondition(dbaasv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("Ready = %+v, want False", cond)
	}
}
