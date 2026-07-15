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
	"testing"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

func TestMarkGenerationReconciledAdvancesObservedGeneration(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Generation = 5

	res := r.markGenerationReconciled(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.ObservedGeneration != 5 {
		t.Fatalf("ObservedGeneration = %d, want 5", inst.Status.ObservedGeneration)
	}
}

func TestMarkGenerationReconciledDoesNotModifyPhase(t *testing.T) {
	r := &DBInstanceReconciler{}
	inst := newProvisionInst()
	inst.Generation = 6
	inst.Status.Phase = dbaasv1.StatusDegraded

	res := r.markGenerationReconciled(context.Background(), inst)

	if res.Outcome != OutcomeSatisfied {
		t.Fatalf("Outcome = %q, want Satisfied", res.Outcome)
	}
	if inst.Status.Phase != dbaasv1.StatusDegraded {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusDegraded)
	}
	if inst.Status.ObservedGeneration != 6 {
		t.Fatalf("ObservedGeneration = %d, want 6", inst.Status.ObservedGeneration)
	}
}
