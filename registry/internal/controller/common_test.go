package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetReady_SetsFieldsCorrectly(t *testing.T) {
	var conds []metav1.Condition
	setReady(&conds, 3, metav1.ConditionTrue, reasonReady, "Harbor is running")

	if len(conds) != 1 {
		t.Fatalf("len(conds) = %d, want 1", len(conds))
	}
	c := conds[0]
	if c.Type != conditionReady {
		t.Errorf("Type = %q, want %q", c.Type, conditionReady)
	}
	if c.Status != metav1.ConditionTrue {
		t.Errorf("Status = %q, want True", c.Status)
	}
	if c.Reason != reasonReady {
		t.Errorf("Reason = %q, want %q", c.Reason, reasonReady)
	}
	if c.Message != "Harbor is running" {
		t.Errorf("Message = %q, want %q", c.Message, "Harbor is running")
	}
	if c.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration = %d, want 3", c.ObservedGeneration)
	}
}

func TestSetReady_OnlyBumpsTransitionTimeOnActualStatusChange(t *testing.T) {
	var conds []metav1.Condition
	setReady(&conds, 1, metav1.ConditionFalse, reasonProvisioning, "waiting for Harbor")
	firstTransition := conds[0].LastTransitionTime

	// Re-running with the SAME status but a different message/generation (the
	// steady-state case: the reconciler patches status every loop) must not
	// look like a fresh transition.
	time.Sleep(2 * time.Millisecond)
	setReady(&conds, 2, metav1.ConditionFalse, reasonProvisioning, "still waiting for Harbor")
	if !conds[0].LastTransitionTime.Equal(&firstTransition) {
		t.Errorf("LastTransitionTime changed on a same-status update: got %v, want unchanged %v",
			conds[0].LastTransitionTime, firstTransition)
	}
	if conds[0].ObservedGeneration != 2 {
		t.Errorf("ObservedGeneration did not update on same-status call: got %d, want 2", conds[0].ObservedGeneration)
	}

	// A genuine status flip (False -> True) must bump the transition time.
	time.Sleep(2 * time.Millisecond)
	setReady(&conds, 3, metav1.ConditionTrue, reasonReady, "Harbor is running")
	if conds[0].LastTransitionTime.Equal(&firstTransition) {
		t.Error("LastTransitionTime did not change on a real status transition (False -> True)")
	}
}
