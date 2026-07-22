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
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

// redactedCloudInitUserData replaces the ephemeral cloud-init Secret's
// userdata once it's no longer needed. A minimal valid no-op cloud-config
// rather than an empty string, in case anything ever re-parses it.
const redactedCloudInitUserData = "#cloud-config\n{}\n"

// ensureBootstrapCleanup redacts the sensitive half of the ephemeral
// cloud-init Secret once the database is provably up. It does NOT delete the
// Secret and does NOT touch the VM.

// A create/update stops the pass so the next reconcile re-observes the
// persisted redaction. Once the Secret is unchanged, the step is Satisfied and
// generation reconciliation may continue.
func (r *DBInstanceReconciler) ensureBootstrapCleanup(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	ciName := inst.Status.Resources.CloudInitSecretName
	if ciName == "" {
		return satisfied() // no VM yet, nothing to redact
	}
	if !inst.Status.IsConditionTrue(dbaasv1.ConditionDatabaseReady) {
		// Not provably consumed yet (booting, stopped, degraded) — defer.
		return satisfied()
	}

	networkdata := credentials.BuildNetworkData(credentials.BootstrapParams{StaticNetwork: inst.Spec.StaticNetwork})
	op, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, resource.CloudInitSecret{
		Instance:    inst,
		UserData:    redactedCloudInitUserData,
		NetworkData: networkdata,
	})
	if err != nil {
		return transient(err)
	}
	if op != controllerutil.OperationResultNone {
		return pending(dbaasv1.ReasonBootstrapCleanupReconciled,
			"cloud-init bootstrap data redacted; waiting for observation")
	}
	return satisfied()
}
