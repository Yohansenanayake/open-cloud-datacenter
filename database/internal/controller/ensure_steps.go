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
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ensureStep is one idempotent unit of the bounded reconcile runner. Each step
// observes real cluster/provider state, diffs it against desired, and returns a
// StepResult — it never consults a status flag as its memory of past actions.
type ensureStep struct {
	name string
	run  func(ctx context.Context, inst *dbaasv1.DBInstance) StepResult
}

// reconcileInstance is Reconcile's entry point for every non-deletion pass. It
// walks the ensure steps, persists status once (MergeFrom + DeepEqual-skip
// via patchStatusIfChanged), then maps the outcome to a controller-runtime
// result:
//
//	Satisfied → zero Result,converged (event-driven; steady state is fully owned by
//	            ensureDatabaseHealth's report-only liveness/crash-loop logic)
//	Pending   → step's Result (zero = watch-driven, or RequeueAfter fallback)
//	Terminal  → park: (ctrl.Result{}, nil); recovers on a spec edit / watch event
//	Transient → return err for controller-runtime backoff
func (r *DBInstanceReconciler) reconcileInstance(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	original := inst.DeepCopy()
	res := r.runEnsureSteps(ctx, inst, r.instanceEnsureSteps())
	r.finalizeStatus(inst)

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

// instanceEnsureSteps is the ordered ensure-step chain the runner walks for
// every DBInstance, in every state — provisioning, steady-state Available,
// and crash-loop-parked alike. There is no separate dispatch for any of
// those; ensureDatabaseHealth's own crash-loop guard and steady-state
// liveness reporting are just more of the chain.
//
// resize runs BEFORE power deliberately: cold resize halts the VM itself and
// stays non-Satisfied while shape drift exists, so the power step never fights
// it; once the shape converges, power observes "desired running, declared
// Halted" and restarts. No cross-step coupling or persisted operation state.
func (r *DBInstanceReconciler) instanceEnsureSteps() []ensureStep {
	return []ensureStep{
		{"finalizer", r.ensureFinalizer},
		{"preflight", r.ensurePreflight},
		{"credentials", r.ensureCredentials},
		{"vm", r.ensureVM},
		{"resize", r.ensureResize},
		{"power", r.ensurePowerState},
		{"health", r.ensureDatabaseHealth},
		{"connection-secret", r.ensureConnectionSecret},
		{"monitoring", r.ensureMonitoring},
		{"bootstrap-cleanup", r.ensureBootstrapCleanup},
		{"generation-reconciled", r.markGenerationReconciled},
	}
}

// runEnsureSteps walks the ordered steps, continuing only while each is Satisfied
// and returning the first non-Satisfied result. If every step is Satisfied, it
// returns satisfied() too — no separate "all steps satisfied" outcome, since a
// chain of steps is itself satisfied precisely when every step in it is. An
// unknown outcome is treated as Transient defensively. Steps are a parameter so
// runner mechanics are testable with injected steps; production callers pass
// r.instanceEnsureSteps().
func (r *DBInstanceReconciler) runEnsureSteps(ctx context.Context, inst *dbaasv1.DBInstance, steps []ensureStep) StepResult {
	logger := log.FromContext(ctx)
	for _, step := range steps {
		res := step.run(ctx, inst)
		switch res.Outcome {
		case OutcomeSatisfied:
			continue
		case OutcomePending, OutcomeTerminal, OutcomeTransient:
			logger.V(1).Info("Ensure step stopped",
				"step", step.name,
				"outcome", res.Outcome,
				"reason", res.Reason,
				"message", res.Message,
				"requeueAfter", res.Result.RequeueAfter,
				"error", res.Err)
			return res
		default:
			res = transient(fmt.Errorf("step %q returned unknown outcome %q", step.name, res.Outcome))
			logger.V(1).Info("Ensure step stopped",
				"step", step.name,
				"outcome", res.Outcome,
				"error", res.Err)
			return res
		}
	}
	return satisfied()
}

// setStepCond sets a status condition and stamps ObservedGeneration =
// inst.Generation (each condition tracks its own observedGeneration).
// inst.Generation is stable for the pass because the runner never re-Gets.
func setStepCond(inst *dbaasv1.DBInstance, condType string, status metav1.ConditionStatus, reason dbaasv1.ConditionReason, msg string) {
	inst.Status.SetCondition(metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             string(reason),
		Message:            msg,
		ObservedGeneration: inst.Generation,
	})
}
