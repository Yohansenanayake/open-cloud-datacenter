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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ensureStep is one idempotent unit of the bounded reconcile runner. Each step
// observes real cluster/provider state, diffs it against desired, and returns a
// StepResult — it never consults a status flag as its memory of past actions.
type ensureStep struct {
	name string
	run  func(ctx context.Context, inst *dbaasv1.DBInstance) StepResult
}

// provisioningSteps is the ordered ensure-step chain the runner walks for
// everything except pure steady-state (phaseAvailable) and parked-failed
// (phaseFailed) instances — those stay legacy until PR6.
//
// resize runs BEFORE power deliberately: cold resize halts the VM itself and
// stays non-Satisfied while shape drift exists, so the power step never fights
// it; once the shape converges, power observes "desired running, declared
// Halted" and restarts. No cross-step coupling or persisted operation state.
func (r *DBInstanceReconciler) provisioningSteps() []ensureStep {
	return []ensureStep{
		{"finalizer", r.ensureFinalizer},
		{"preflight", r.ensurePreflight},
		{"credentials", r.ensureCredentials},
		{"vm", r.ensureVM},
		{"resize", r.ensureStorageResize},
		{"power", r.ensurePowerState},
		{"health", r.ensureDatabaseHealth},
		{"monitoring", r.ensureMonitoring},
		{"ready", r.ensureReady},
	}
}

// runEnsureSteps walks the ordered steps, continuing only while each is Satisfied
// and returning the first non-Satisfied result. An unknown outcome is treated as
// Transient defensively. Steps are a parameter so runner mechanics are testable
// with injected steps; production callers pass r.provisioningSteps().
func (r *DBInstanceReconciler) runEnsureSteps(ctx context.Context, inst *dbaasv1.DBInstance, steps []ensureStep) StepResult {
	for _, step := range steps {
		res := step.run(ctx, inst)
		switch res.Outcome {
		case OutcomeSatisfied:
			continue
		case OutcomePending, OutcomeTerminal, OutcomeTransient:
			return res
		default:
			return transient(fmt.Errorf("step %q returned unknown outcome %q", step.name, res.Outcome))
		}
	}
	return satisfied()
}

// runProvisioning is the bounded-reconcile entry point for the provisioning window.
// It walks the ensure steps, persists status once (MergeFrom + DeepEqual-skip via the
// PR2 helper), then maps the outcome to a controller-runtime result:
//
//	Satisfied → zero Result (event-driven; ensureReady handed off to phaseAvailable)
//	Pending   → step's Result (zero = watch-driven, or RequeueAfter fallback)
//	Terminal  → park: (ctrl.Result{}, nil); recovers on a spec edit / watch event
//	Transient → return err for controller-runtime backoff
func (r *DBInstanceReconciler) runProvisioning(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	original := inst.DeepCopy()
	res := r.runEnsureSteps(ctx, inst, r.provisioningSteps())

	if err := r.patchStatusIfChanged(ctx, original, inst); err != nil {
		return ctrl.Result{}, err
	}

	switch res.Outcome {
	case OutcomeTerminal:
		return ctrl.Result{}, nil
	case OutcomeTransient:
		return ctrl.Result{}, res.Err
	default:
		return res.Result, nil
	}
}

// setStepCond sets a status condition and stamps ObservedGeneration = inst.Generation
// (per-condition observedGeneration; plan §8.1). inst.Generation is stable for the
// pass because the runner never re-Gets.
func setStepCond(inst *dbaasv1.DBInstance, condType string, status metav1.ConditionStatus, reason, msg string) {
	inst.Status.SetCondition(metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: inst.Generation,
	})
}

// markProvisioningFailed records a user-facing terminal failure for a step in the
// provisioning window. It deliberately does NOT move ProvisioningPhase out of the
// provisioning window, so the runner keeps ownership and re-runs (and can recover)
// on the next spec edit / watch event — unlike the legacy fail(), which routes to
// phaseFailed (that path expects a VM to probe, which a preflight failure has not
// created).
func markProvisioningFailed(inst *dbaasv1.DBInstance, reason, msg string) {
	inst.Status.Phase = dbaasv1.StatusFailed
	inst.Status.Message = msg
	setStepCond(inst, dbaasv1.ConditionFailed, metav1.ConditionTrue, reason, msg)
}
