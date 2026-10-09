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
	goerrors "errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// reconcileInstance runs the non-deletion ensure workflow. The top-level
// Reconcile defer derives aggregate status and persists all status changes.
func (r *DBInstanceReconciler) reconcileInstance(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if r.EnsureRunner == nil {
		return ctrl.Result{}, fmt.Errorf("ensure runner is not configured")
	}

	result := r.EnsureRunner.Run(ctx, inst)

	scheduleRequeue, err := r.evaluateBackupSchedule(ctx, inst)
	if err != nil {
		return result.ControllerResult, goerrors.Join(result.Err, err)
	}

	ctrlResult := result.ControllerResult
	if scheduleRequeue > 0 && (ctrlResult.RequeueAfter == 0 || scheduleRequeue < ctrlResult.RequeueAfter) {
		ctrlResult.RequeueAfter = scheduleRequeue
	}
	return ctrlResult, result.Err
}
