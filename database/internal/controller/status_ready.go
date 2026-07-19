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

// syncReadyCondition recomputes Ready every pass, independent of where the
// ensure-step chain stopped this time — a pure derivation from currently-known
// status, safe and cheap to run unconditionally. It aggregates the required
// operational product stack: PostgreSQL reachability and monitoring-resource
// convergence. It deliberately does NOT check Accepted or
// StorageReady/PreflightReady directly — a rejected request
// (invalid class, immutable-field edit, unsupported shrink) never touches the
// VM, so DatabaseReady/Ready correctly stay whatever they already were.
// Accepted and phase surface the rejection independently.
//
// Ready is the summary condition external tooling conventionally looks for
// (kubectl wait --for=condition=Ready, kstatus-style dashboards).
// DatabaseReady remains the narrower signal for clients that need to know
// whether PostgreSQL itself is usable even when another required product
// component is degraded.
func (r *DBInstanceReconciler) syncReadyCondition(inst *dbaasv1.DBInstance) {
	if !inst.DeletionTimestamp.IsZero() {
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse, dbaasv1.ReasonDeleting, "instance is deleting")
		return
	}
	if !wantRunning(inst) {
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse, dbaasv1.ReasonStopped, "instance is deliberately stopped")
		return
	}

	// Use the latest operational observation, not GetCurrentCondition: a spec
	// edit can be rejected while the existing database remains healthy.
	dbReady := inst.Status.GetCondition(dbaasv1.ConditionDatabaseReady)
	if dbReady == nil || dbReady.Status != metav1.ConditionTrue {
		reason, msg := dbaasv1.ReasonProvisioning, "database not yet ready"
		if dbReady != nil {
			if parsed, ok := dbaasv1.ParseConditionReason(dbReady.Reason); ok {
				reason = parsed
			}
			msg = dbReady.Message
		}
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse, reason, msg)
		return
	}

	monitoringReady := inst.Status.GetCondition(dbaasv1.ConditionMonitoringReady)
	if monitoringReady == nil || monitoringReady.Status != metav1.ConditionTrue {
		reason, msg := dbaasv1.ReasonProvisioning, "monitoring not yet ready"
		if monitoringReady != nil {
			if parsed, ok := dbaasv1.ParseConditionReason(monitoringReady.Reason); ok {
				reason = parsed
			}
			msg = monitoringReady.Message
		}
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse, reason, msg)
		return
	}

	setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "database and monitoring ready")
}
