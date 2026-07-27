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

package ensure

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// Outcome describes how an ensure step completed.
type Outcome string

const (
	OutcomeSatisfied Outcome = "Satisfied"
	OutcomePending   Outcome = "Pending"
	OutcomeTerminal  Outcome = "Terminal"
	OutcomeTransient Outcome = "Transient"
)

// Result is the outcome of one ensure step.
type Result struct {
	Outcome          Outcome
	ControllerResult ctrl.Result
	Err              error
	Reason           dbaasv1.ConditionReason
	Message          string
}

func Satisfied() Result { return Result{Outcome: OutcomeSatisfied} }

func Pending(reason dbaasv1.ConditionReason, message string) Result {
	return Result{Outcome: OutcomePending, Reason: reason, Message: message}
}

func PendingAfter(reason dbaasv1.ConditionReason, message string, after time.Duration) Result {
	return Result{
		Outcome:          OutcomePending,
		Reason:           reason,
		Message:          message,
		ControllerResult: ctrl.Result{RequeueAfter: after},
	}
}

func Terminal(reason dbaasv1.ConditionReason, message string) Result {
	return Result{Outcome: OutcomeTerminal, Reason: reason, Message: message}
}

func Transient(err error) Result { return Result{Outcome: OutcomeTransient, Err: err} }
