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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ensureBootstrapCleanup is the home for one-shot cleanups of bootstrap-only
// artifacts once the database is provably up (future first-boot cleanups land
// here too). Today it owns the ephemeral cloud-init Secret: the disk reference is
// removed from the VM spec first (otherwise a later VMI restart hits FailedMount
// on the deleted Secret — see RemoveCloudInitDisk's doc comment), then the Secret
// (a plain corev1.Secret — not a Harvester resource, so deleted via the
// controller's own client rather than r.Harvester) is deleted and the ref cleared.
//
// It acts only while DatabaseReady=True: a passing in-guest probe proves
// cloud-init was consumed, so deleting it can no longer break a first boot.
func (r *DBInstanceReconciler) ensureBootstrapCleanup(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	ciName := inst.Status.Resources.CloudInitSecretName
	if ciName == "" {
		return satisfied() // nothing left to scrub
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		// Not provably consumed yet (booting, stopped, degraded) — defer.
		return satisfied()
	}

	if err := r.Harvester.RemoveCloudInitDisk(ctx, inst.Namespace, vmNameFor(inst)); err != nil {
		return transient(err)
	}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: ciName}}
	if err := r.Delete(ctx, sec); err != nil && !apierrors.IsNotFound(err) {
		return transient(err)
	}
	inst.Status.Resources.CloudInitSecretName = ""
	return pending("CloudInitScrubbed", "removed bootstrap cloud-init disk and secret")
}
