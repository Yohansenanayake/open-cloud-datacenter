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
)

// StepOutcome is the result kind of one ensure step. It drives the bounded
// reconcile runner: only OutcomeSatisfied continues to the next step; every other
// outcome stops the pass after status is patched.
type StepOutcome string

const (
	// OutcomeSatisfied: observed state already matches desired. Continue.
	OutcomeSatisfied StepOutcome = "Satisfied"
	// OutcomePending: not converged yet — the step made a change, or is waiting on
	// asynchronous runtime state. Patch status and stop. The requeue policy lives on
	// StepResult.Result: a zero Result is event-driven (rely on Owns()/VMI watches);
	// a RequeueAfter is a timer fallback for a watch-less wait. Pending merges the
	// former Progress ("made a change") and Waiting ("waiting on runtime"): they did
	// the identical thing in the runner and differed only in requeue policy.
	OutcomePending StepOutcome = "Pending"
	// OutcomeTerminal: current spec/config cannot be reconciled without user action.
	// Park — no error, no requeue; recovers on a spec edit / watch event.
	OutcomeTerminal StepOutcome = "Terminal"
	// OutcomeTransient: retryable failure. Return Err for controller-runtime backoff.
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
	Reason  string
	Message string
}

// satisfied reports that observed state already matches desired; the runner
// continues to the next step.
func satisfied() StepResult { return StepResult{Outcome: OutcomeSatisfied} }

// pending reports that the step made a change this pass. The zero Result makes it
// event-driven: the mutation's Owns()/VMI watch (and the status patch) re-trigger
// the next reconcile.
func pending(reason, msg string) StepResult {
	return StepResult{Outcome: OutcomePending, Reason: reason, Message: msg}
}

// pendingAfter reports that the step is waiting on asynchronous state that may emit
// no watch event; RequeueAfter is a timer fallback.
func pendingAfter(reason, msg string, after time.Duration) StepResult {
	return StepResult{
		Outcome: OutcomePending,
		Reason:  reason,
		Message: msg,
		Result:  ctrl.Result{RequeueAfter: after},
	}
}

// terminal reports that the current spec cannot be reconciled without user action;
// the runner parks (no error, no requeue).
func terminal(reason, msg string) StepResult {
	return StepResult{Outcome: OutcomeTerminal, Reason: reason, Message: msg}
}

// transient reports a retryable failure; the runner returns Err for backoff.
func transient(err error) StepResult { return StepResult{Outcome: OutcomeTransient, Err: err} }
