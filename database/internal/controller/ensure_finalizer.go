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
// resources are created. Reconcile also adds the finalizer pre-dispatch (and
// returns early), so in practice this step observes an already-present finalizer;
// it exists so the runner is self-sufficient if that pre-dispatch ever moves.
func (r *DBInstanceReconciler) ensureFinalizer(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	if controllerutil.ContainsFinalizer(inst, dbaasv1.FinalizerName) {
		return satisfied()
	}
	controllerutil.AddFinalizer(inst, dbaasv1.FinalizerName)
	if err := r.Update(ctx, inst); err != nil {
		return transient(err)
	}
	// The metadata update generates a watch event on the DBInstance itself.
	return pending("FinalizerAdded", "added cleanup finalizer")
}
