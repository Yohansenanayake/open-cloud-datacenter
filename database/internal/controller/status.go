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
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// patchStatusIfChanged persists inst.Status with an optimistic-lock MergeFrom
// patch, skipping the write when the status is unchanged.
//
// The desired status is captured before retrying. Each attempt re-fetches the
// latest object and uses its resourceVersion as a precondition, so a concurrent
// write produces a conflict and retries from a fresh object. The DeepEqual skip
// prevents an unchanged status from triggering a self-reconcile loop.
//
// DBInstance status is currently owned in full by this controller, so each
// attempt applies the complete desired status produced by the ensure pass. If
// DBaaS expands to multiple controllers that share status ownership, replace
// this whole-status assignment with semantic transactions replayed against the
// latest object so one controller preserves fields owned by another.
//
// `original` must be the DeepCopy captured at the top of Reconcile (before the
// pass mutated inst.Status).
func (r *DBInstanceReconciler) patchStatusIfChanged(ctx context.Context, original, inst *dbaasv1.DBInstance) error {
	if equality.Semantic.DeepEqual(original.Status, inst.Status) {
		return nil
	}

	desiredStatus := inst.Status.DeepCopy()
	key := client.ObjectKeyFromObject(inst)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dbaasv1.DBInstance{}
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		if equality.Semantic.DeepEqual(latest.Status, *desiredStatus) {
			return nil
		}

		base := latest.DeepCopy()
		latest.Status = *desiredStatus.DeepCopy()
		if err := r.Status().Patch(ctx, latest, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		inst.Status = *latest.Status.DeepCopy()
		return nil
	})
}
