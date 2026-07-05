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
// (plan §8.1) and where Ready=True is stamped. Setting ProvisioningPhase=Available
// hands steady-state ownership to the legacy phaseAvailable dispatch (liveness,
// crash-loop, endpoint refresh) until PR6 folds that into ensureDatabaseHealth.
func (r *DBInstanceReconciler) ensureReady(_ context.Context, inst *dbaasv1.DBInstance) StepResult {
	inst.Status.Phase = dbaasv1.StatusAvailable
	inst.Status.ProvisioningPhase = dbaasv1.PhaseAvailable
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.Message = "Database instance is available"
	setStepCond(inst, dbaasv1.ConditionReady, metav1.ConditionTrue,
		"DBInstanceReady", "all ensure steps satisfied")
	return satisfied()
}
