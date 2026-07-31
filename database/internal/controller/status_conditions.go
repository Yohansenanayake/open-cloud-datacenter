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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func conditionReason(c *metav1.Condition) dbaasv1.ConditionReason {
	if c != nil {
		if reason, ok := dbaasv1.ParseConditionReason(c.Reason); ok {
			return reason
		}
	}
	return dbaasv1.ReasonUnknownValidationFailure
}

func (r *DBInstanceReconciler) syncAcceptedCondition(inst *dbaasv1.DBInstance) {
	preflight := inst.Status.GetCurrentCondition(dbaasv1.ConditionPreflightReady, inst.Generation)
	if preflight == nil || preflight.Status == metav1.ConditionUnknown {
		inst.SetCurrentCondition(dbaasv1.ConditionAccepted, metav1.ConditionUnknown,
			dbaasv1.ReasonValidationPending, "current specification has not been fully evaluated")
		return
	}
	if preflight.Status == metav1.ConditionFalse {
		inst.SetCurrentCondition(dbaasv1.ConditionAccepted, metav1.ConditionFalse,
			conditionReason(preflight), preflight.Message)
		return
	}

	// StorageChangeRejected is abnormal-only. Absence means there is no known
	// current-generation rejection; it does not require a positive counterpart.
	if rejected := inst.Status.GetCurrentCondition(dbaasv1.ConditionStorageChangeRejected, inst.Generation); rejected != nil {
		switch rejected.Status {
		case metav1.ConditionTrue:
			inst.SetCurrentCondition(dbaasv1.ConditionAccepted, metav1.ConditionFalse,
				conditionReason(rejected), rejected.Message)
			return
		case metav1.ConditionUnknown:
			inst.SetCurrentCondition(dbaasv1.ConditionAccepted, metav1.ConditionUnknown,
				dbaasv1.ReasonValidationPending, "storage change validation is incomplete")
			return
		}
	}

	inst.SetCurrentCondition(dbaasv1.ConditionAccepted, metav1.ConditionTrue,
		dbaasv1.ReasonSpecAccepted, "current specification is accepted")
}

func (r *DBInstanceReconciler) syncInterventionRequiredCondition(inst *dbaasv1.DBInstance) {
	if inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		inst.SetCurrentCondition(dbaasv1.ConditionInterventionRequired, metav1.ConditionTrue,
			dbaasv1.ReasonInterventionRequired,
			conditionMessage(inst, dbaasv1.ConditionCrashLoopHalted, "operator intervention required"))
		return
	}
	inst.SetCurrentCondition(dbaasv1.ConditionInterventionRequired, metav1.ConditionFalse,
		dbaasv1.ReasonNoInterventionRequired, "no operator intervention required")
}

// syncResizeInProgressCondition clears ResizeInProgress only after the current
// shape has converged and the instance has settled: Ready when running, or
// PowerStateReady when stopped. This keeps a resize in progress condition while the VM
// or PostgreSQL is still recovering from the shape change.
func (r *DBInstanceReconciler) syncResizeInProgressCondition(inst *dbaasv1.DBInstance) {
	// A resize cannot complete until the requested shape has converged for this generation.
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionResizeInProgress) ||
		!inst.Status.IsCurrentConditionTrue(dbaasv1.ConditionStorageReady, inst.Generation) {
		return
	}

	if inst.Spec.WantRunning() {
		if inst.Status.IsCurrentConditionTrue(dbaasv1.ConditionReady, inst.Generation) {
			inst.Status.RemoveCondition(dbaasv1.ConditionResizeInProgress)
		}
		return
	}

	// A User Stopped instance also can be resized
	if inst.Status.IsCurrentConditionTrue(dbaasv1.ConditionPowerStateReady, inst.Generation) {
		inst.Status.RemoveCondition(dbaasv1.ConditionResizeInProgress)
	}
}

// syncRepaveInProgressCondition clears RepaveInProgress only once the drift
// it was addressing is gone and the instance has settled: Ready when
// running, or PowerStateReady when stopped. Mirrors
// syncResizeInProgressCondition's pattern — ConditionImageDrift's ABSENCE is
// the positive "converged" signal here, the role ConditionStorageReady=True
// plays for resize (ImageDrift has no explicit False state; it's either True
// or removed entirely).
func (r *DBInstanceReconciler) syncRepaveInProgressCondition(inst *dbaasv1.DBInstance) {
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionRepaveInProgress) ||
		inst.Status.GetCondition(dbaasv1.ConditionImageDrift) != nil {
		return
	}

	if inst.Spec.WantRunning() {
		if inst.Status.IsCurrentConditionTrue(dbaasv1.ConditionReady, inst.Generation) {
			inst.Status.RemoveCondition(dbaasv1.ConditionRepaveInProgress)
		}
		return
	}

	// A user-stopped instance can also be repaved.
	if inst.Status.IsCurrentConditionTrue(dbaasv1.ConditionPowerStateReady, inst.Generation) {
		inst.Status.RemoveCondition(dbaasv1.ConditionRepaveInProgress)
	}
}

func conditionMessage(inst *dbaasv1.DBInstance, typ, fallback string) string {
	if c := inst.Status.GetCondition(typ); c != nil && c.Message != "" {
		return c.Message
	}
	return fallback
}

func (r *DBInstanceReconciler) finalizeStatus(inst *dbaasv1.DBInstance) {
	r.syncAcceptedCondition(inst)
	r.syncReadyCondition(inst)
	r.syncResizeInProgressCondition(inst)     // aggregate resize lifecycle into a single condition
	r.syncRepaveInProgressCondition(inst)     // aggregate repave lifecycle into a single condition
	r.syncInterventionRequiredCondition(inst) // aggregation of crash-loop halt and other intervention-required conditions
	summary := dbaasv1.DerivePhaseSummary(inst)
	inst.Status.Phase = summary.Phase
	inst.Status.Message = summary.Message
}
