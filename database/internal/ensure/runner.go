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
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// Runner executes ordered steps until one is not satisfied.
type Runner struct {
	steps []Step
}

func NewRunner(steps ...Step) *Runner { return &Runner{steps: steps} }

// NewDefaultSteps returns the production DBInstance step chain in execution order.
func NewDefaultSteps(deps Dependencies) []Step {
	return []Step{
		newPreflightStep(deps),
		newCredentialsStep(deps),
		newVMStep(deps),
		newResizeStep(deps),
		newPowerStep(deps),
		newHealthStep(deps),
		newConnectionSecretStep(deps),
		newMonitoringStep(deps),
		newBootstrapCleanupStep(deps),
		newGenerationStep(),
	}
}

func NewDefaultRunner(deps Dependencies) *Runner {
	return NewRunner(NewDefaultSteps(deps)...)
}

func (r *Runner) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	logger := log.FromContext(ctx)
	for _, step := range r.steps {
		result := step.Run(ctx, inst)
		switch result.Outcome {
		case OutcomeSatisfied:
			continue
		case OutcomePending, OutcomeTerminal, OutcomeTransient:
			logger.V(1).Info("Ensure step stopped", "step", step.Name(), "outcome", result.Outcome,
				"reason", result.Reason, "message", result.Message,
				"requeueAfter", result.ControllerResult.RequeueAfter, "error", result.Err)
			return result
		default:
			return Transient(fmt.Errorf("step %q returned unknown outcome %q", step.Name(), result.Outcome))
		}
	}
	return Satisfied()
}
