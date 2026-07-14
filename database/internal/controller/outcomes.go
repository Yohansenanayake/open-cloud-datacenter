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
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// StepOutcome is the result kind of one ensure step. Only Satisfied continues
// to the next step; every other outcome stops the pass after status is patched.
//
//   - Satisfied: nothing left to do this pass — converged, or a report-only /
//     non-fatal concern that shouldn't block the chain. Also what the whole
//     chain reports once every step in it is Satisfied (see runEnsureSteps).
//   - Pending: made a change, or waiting on async state. StepResult.Result
//     carries the requeue policy: zero is event-driven, RequeueAfter is a
//     timer fallback.
//   - Terminal: needs user action to unblock. Park — no error, no requeue.
//   - Transient: retryable failure. Return Err for controller-runtime backoff.
type StepOutcome string

const (
	OutcomeSatisfied StepOutcome = "Satisfied"
	OutcomePending   StepOutcome = "Pending"
	OutcomeTerminal  StepOutcome = "Terminal"
	OutcomeTransient StepOutcome = "Transient"
)

// StepResult is what an ensure step returns to the runner.
type StepResult struct {
	Outcome StepOutcome

	// Result is honored for OutcomePending: a zero Result is event-driven (rely on
	// Owns()/VMI watches to re-trigger); a RequeueAfter is a timer fallback for a
	// wait that may emit no watch event.
	Result ctrl.Result

	// Err is set only for OutcomeTransient; the runner returns it for backoff.
	Err error

	// Reason and Message describe the outcome for status conditions / events. Reason
	// carries the "creating" vs "waiting" nuance that a single OutcomePending no
	// longer encodes in the enum.
	Reason  dbaasv1.ConditionReason
	Message string
}

// satisfied reports that this step has no further action to take this pass;
// the runner continues to the next step.
func satisfied() StepResult { return StepResult{Outcome: OutcomeSatisfied} }

// pending reports that the step made a change this pass. The zero Result makes it
// event-driven: the mutation's Owns()/VMI watch (and the status patch) re-trigger
// the next reconcile.
func pending(reason dbaasv1.ConditionReason, msg string) StepResult {
	return StepResult{Outcome: OutcomePending, Reason: reason, Message: msg}
}

// pendingAfter reports that the step is waiting on asynchronous state that may emit
// no watch event; RequeueAfter is a timer fallback.
func pendingAfter(reason dbaasv1.ConditionReason, msg string, after time.Duration) StepResult {
	return StepResult{
		Outcome: OutcomePending,
		Reason:  reason,
		Message: msg,
		Result:  ctrl.Result{RequeueAfter: after},
	}
}

// terminal reports that the current spec cannot be reconciled without user action;
// the runner parks (no error, no requeue).
func terminal(reason dbaasv1.ConditionReason, msg string) StepResult {
	return StepResult{Outcome: OutcomeTerminal, Reason: reason, Message: msg}
}

// transient reports a retryable failure; the runner returns Err for backoff.
func transient(err error) StepResult { return StepResult{Outcome: OutcomeTransient, Err: err} }
