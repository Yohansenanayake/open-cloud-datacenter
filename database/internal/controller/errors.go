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
	"errors"
	"fmt"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// TerminalError marks a spec/config problem that retrying the same spec cannot fix
// (e.g. an unknown instance class, an unsupported storage shrink). A step may
// return it from deep in its call stack; classifyError maps it to OutcomeTerminal
// so the runner parks instead of hot-looping on a change that can never succeed.
type TerminalError struct {
	Reason  dbaasv1.ConditionReason
	Message string
}

func (e *TerminalError) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason, e.Message)
}

// Assert TerminalError satisfies the error interface.
var _ error = (*TerminalError)(nil)

// terminalErr builds a TerminalError for a spec/config problem.
func terminalErr(reason dbaasv1.ConditionReason, msg string) error {
	return &TerminalError{Reason: reason, Message: msg}
}

// classifyError maps an arbitrary error to a StepResult. This is the default
// taxonomy: a *TerminalError (even wrapped with %w) becomes OutcomeTerminal, and
// every other error defaults to OutcomeTransient for controller-runtime backoff.
// A step calls this when a helper returns a raw error and it wants terminal
// classification to bubble up while unknown failures stay retryable. Callers must
// pass a non-nil error.
func classifyError(err error) StepResult {
	var te *TerminalError
	if errors.As(err, &te) {
		return terminal(te.Reason, te.Message)
	}
	return transient(err)
}
