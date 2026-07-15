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

	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// patchStatusIfChanged persists inst.Status with a MergeFrom patch, skipping the
// write when the status is unchanged.
//
// Mechanism: only this controller writes DBInstance status, and controller-runtime
// serializes reconciles per object, so there is a single writer — no optimistic
// lock or re-Get is needed. Because we never re-Get during a pass, inst.Generation
// is stable, so steps (and markGenerationReconciled) can stamp observedGeneration from it
// directly. The DeepEqual skip prevents an unchanged status from triggering a
// self-reconcile loop.
//
// `original` must be the DeepCopy captured at the top of Reconcile (before the
// pass mutated inst.Status).
func (r *DBInstanceReconciler) patchStatusIfChanged(ctx context.Context, original, inst *dbaasv1.DBInstance) error {
	if equality.Semantic.DeepEqual(original.Status, inst.Status) {
		return nil
	}
	return r.Status().Patch(ctx, inst, client.MergeFrom(original))
}
