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

	ctrl "sigs.k8s.io/controller-runtime"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// reconcileInstance runs the non-deletion ensure workflow, derives aggregate
// status, and persists status once per pass.
func (r *DBInstanceReconciler) reconcileInstance(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if r.EnsureRunner == nil {
		return ctrl.Result{}, fmt.Errorf("ensure runner is not configured")
	}

	original := inst.DeepCopy()
	result := r.EnsureRunner.Run(ctx, inst)
	r.finalizeStatus(inst)

	if err := r.patchStatusIfChanged(ctx, original, inst); err != nil {
		return ctrl.Result{}, err
	}

	return result.ControllerResult, result.Err
}
