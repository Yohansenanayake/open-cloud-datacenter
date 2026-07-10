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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ensureReady runs only when every prior ensure step returned Satisfied for this
// pass. It is the single place the top-level status.observedGeneration advances
// and where Ready is stamped for the converged state — Available for a running
// instance, Stopped for a deliberately stopped one (Ready=False, never
// stale-True). Setting Status.Phase here is just a status value; there's no
// separate dispatch keyed on it — every subsequent pass walks the same
// ensure-step chain, where ensureDatabaseHealth owns steady-state liveness,
// crash-loop handling, and endpoint refresh, and the whole chain idles cold
// (all steps Satisfied, no writes) once nothing has changed.
func (r *DBInstanceReconciler) ensureReady(_ context.Context, inst *dbaasv1.DBInstance) StepResult {
	if !wantRunning(inst) {
		inst.Status.Phase = dbaasv1.StatusStopped
		inst.Status.ObservedGeneration = inst.Generation
		inst.Status.Message = "Stopped. Storage preserved."
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse,
			"Stopped", "instance is deliberately stopped")
		return satisfied()
	}

	inst.Status.ObservedGeneration = inst.Generation

	// A caught-up probe blip reaches here as report-only (health returned
	// Satisfied with DatabaseReady=False and Phase=degraded). Ready must reflect
	// that — never stale-True — and phase stays "degraded" as health set it.
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse,
			"Degraded", "database degraded; see the Degraded condition for attribution")
		return satisfied()
	}

	inst.Status.Phase = dbaasv1.StatusAvailable
	inst.Status.Message = "Database instance is available"
	setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionTrue,
		"DBInstanceReady", "all ensure steps satisfied")
	return satisfied()
}
