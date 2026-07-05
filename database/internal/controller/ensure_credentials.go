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

// ensureCredentials is a PR4 shim. Credential material (passwords, TLS) and the
// credentials/cloud-init Secrets are still generated inside the Harvester client's
// CreatePostgresVM, which is idempotent and reuses existing Secret material on
// re-entry — so there is nothing for this step to reconcile yet. PR8 moves
// credential resolution here (internal/credentials Resolver + Secret builders) and
// this step becomes a real observe→diff→outcome step.
func (r *DBInstanceReconciler) ensureCredentials(_ context.Context, inst *dbaasv1.DBInstance) StepResult {
	if inst.Status.Resources.SecretName != "" {
		setStepCond(inst, dbaasv1.ConditionCredentialsReady, metav1.ConditionTrue,
			"CredentialsProvisioned", "admin credentials secret present")
	}
	return satisfied()
}
