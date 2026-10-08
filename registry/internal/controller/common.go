// Package controller holds the reconciler for the registry operator: Registry,
// a project inside the central Harbor, and the only resource users create. The
// Custom Resource is the single source of truth, and all work happens inside
// the reconcile loop, with slow waits handled via RequeueAfter.
package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// planOrder lists the plans from smallest to largest. The first entry is the
// default a Registry gets when its spec leaves the plan empty.
var planOrder = []string{"starter", "professional", "enterprise"}

const (
	phaseProvisioning = "Provisioning"
	phaseReady        = "Ready"
	phaseFailed       = "Failed"
	phaseTerminating  = "Terminating"

	conditionReady = "Ready"

	// Event actions name what the operator did to the object, which the events
	// API requires alongside the reason. UpperCamelCase, same convention as
	// reasons.
	actionReconcile = "Reconcile"
	actionProvision = "Provision"
	actionDelete    = "Delete"

	// eventReasonReady etc. are CamelCase to satisfy the condition-reason
	// and Event-reason conventions.
	reasonReady        = "Ready"
	reasonProvisioning = "Provisioning"
	reasonTransient    = "Transient"
	reasonError        = "Error"
)

// setReady sets/updates the Ready condition. It delegates to
// meta.SetStatusCondition, which only bumps LastTransitionTime when the status
// actually changes (the correct K8s semantics) and records observedGeneration.
func setReady(conds *[]metav1.Condition, gen int64, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: gen,
	})
}
