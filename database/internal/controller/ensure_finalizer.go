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

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ensureFinalizer guarantees the cleanup finalizer is present before any child
// resources are created. Being first in the chain, it's what actually adds the
// finalizer on a brand-new DBInstance's first pass — Reconcile has no separate
// pre-dispatch for this, matching every other concern in the chain.
func (r *DBInstanceReconciler) ensureFinalizer(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	if controllerutil.ContainsFinalizer(inst, dbaasv1.FinalizerName) {
		return satisfied()
	}
	controllerutil.AddFinalizer(inst, dbaasv1.FinalizerName)
	if err := r.Update(ctx, inst); err != nil {
		return transient(err)
	}
	// The metadata update generates a watch event on the DBInstance itself. NO need to explicitly requeue.
	// Pending returns a Zero ControllerResult, - no explicit requeue.
	return pending("FinalizerAdded", "added cleanup finalizer")
}
