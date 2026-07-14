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
// and status.phase is stamped Available/Stopped for the converged state — that
// specifically means "the whole chain converged for this generation", which is
// why it's gated: ensureDatabaseHealth's own caughtUp check depends on
// ObservedGeneration only ever advancing once a pass has actually gone all the
// way through. The Ready *condition* is a different question — "is the
// database reachable right now" — and is deliberately NOT set here; see
// syncReadyCondition, which runs unconditionally every pass regardless of
// where the chain stopped, so Ready can never go stale while parked (e.g.
// crash-loop halted, mid-resize, or any other Terminal/Pending park).
func (r *DBInstanceReconciler) ensureReady(_ context.Context, inst *dbaasv1.DBInstance) StepResult {
	if !wantRunning(inst) {
		inst.Status.ObservedGeneration = inst.Generation
		return satisfied()
	}

	inst.Status.ObservedGeneration = inst.Generation

	if !inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		return satisfied()
	}

	return satisfied()
}

// syncReadyCondition recomputes Ready every pass, independent of where the
// ensure-step chain stopped this time — a pure derivation from currently-known
// status, safe and cheap to run unconditionally. It intentionally checks only
// wantRunning and DatabaseReady: DatabaseReady is kept honest by whichever step
// actually takes the VM down (ensureDatabaseHealth's crash-loop halt,
// ensureResize's cold-resize halt, ensurePowerState's stop transition), so
// Ready never needs to special-case those itself. It deliberately does NOT
// check Accepted or StorageReady/PreflightReady directly — a rejected request
// (invalid class, immutable-field edit, unsupported shrink) never touches the
// VM, so DatabaseReady/Ready correctly stay whatever they already were.
// Accepted and phase surface the rejection independently.
//
// Today Ready reduces to "DatabaseReady plus the wantRunning override" — a
// deliberate choice, not evidence it's redundant. Ready is the summary
// condition external tooling conventionally looks for (kubectl wait
// --for=condition=Ready, kstatus-style dashboards); DatabaseReady is a
// narrower, single-purpose signal about Postgres's own reachability. Keeping
// them distinct now means a future gating concern that isn't about Postgres
// being reachable (e.g. a fatal TLS rotation failure) can fold into this
// function without redefining what DatabaseReady means.
func (r *DBInstanceReconciler) syncReadyCondition(inst *dbaasv1.DBInstance) {
	if !inst.DeletionTimestamp.IsZero() {
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse, dbaasv1.ReasonDeleting, "instance is deleting")
		return
	}
	if !wantRunning(inst) {
		setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionFalse, dbaasv1.ReasonStopped, "instance is deliberately stopped")
		return
	}

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

	setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionTrue, dbaasv1.ReasonDBInstanceReady, "database ready")
}
