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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

type generationStep struct{}

func newGenerationStep() Step { return generationStep{} }

func (generationStep) Name() string { return "generation-reconciled" }

// markGenerationReconciled runs only after every preceding ensure step has
// returned Satisfied. Reaching this final checkpoint means the requested
// generation has fully converged, so it is safe to advance observedGeneration.
func (generationStep) Run(_ context.Context, inst *dbaasv1.DBInstance) Result {
	inst.Status.ObservedGeneration = inst.Generation
	return Satisfied()
}
