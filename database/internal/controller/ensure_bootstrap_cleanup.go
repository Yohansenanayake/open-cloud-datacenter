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
//
// It used to delete the Secret outright (and strip the disk reference from
// the VM template first, to keep a future VMI restart from trying to mount
// a Secret that was about to disappear). That broke in practice: a live
// VMI/pod's mounted volumes are immutable — patching the VM template never
// affects the *already-running* pod, only some hypothetical future VMI
// generation. So the running pod kept referencing the cloud-init volume for
// its whole life, and once the Secret was deleted, kubelet's periodic
// Secret-volume re-sync kept failing with FailedMount for as long as that
// pod lived (harmless to the already-mounted data, but noisy indefinitely).
//
// Redacting in place instead: `userdata` carries the actual sensitive
// bootstrap material (admin/repl/exporter passwords, TLS private key) via
// cloud-init's runcmd/write_files modules, which run at the default
// once-per-instance frequency — provably never re-consulted on a
// same-instance reboot, so blanking it is safe. `networkdata` (netplan
// config) is left byte-for-byte correct: cloud-init's network stage is one
// of the few that commonly runs on every boot, so its content is kept valid
// rather than risking a future boot losing static IP/gateway/DNS config.
// The Secret object itself is never deleted, so there's nothing left for
// kubelet to fail to mount, ever.
//
// Runs every pass once DatabaseReady, not once — matching the same
// cheap-same-pass exception ensureMonitoring documents: resource.Apply
// already no-ops when content is unchanged, so there's no need for one-shot
// bookkeeping.
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
	if _, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, resource.CloudInitSecret{
		Instance:    inst,
		UserData:    redactedCloudInitUserData,
		NetworkData: networkdata,
	}); err != nil {
		return transient(err)
	}
	return satisfied()
}
