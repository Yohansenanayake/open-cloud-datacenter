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

package ensure

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/credentials"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/resource"
)

type vmStep struct{ Dependencies }

func newVMStep(deps Dependencies) Step { return &vmStep{Dependencies: deps} }

func (*vmStep) Name() string { return "vm" }

// vmNameFor returns the deterministic VM name for an instance. It matches the
// name CreatePostgresVM derives ("pg-<id>"), preferring the recorded ref for
// instances created before the convention existed.
func vmNameFor(inst *dbaasv1.DBInstance) string {
	if inst.Status.Resources.VMName != "" {
		return inst.Status.Resources.VMName
	}
	return fmt.Sprintf("pg-%s", inst.Name)
}

// dataVolumeNameFor preserves a recorded legacy name and otherwise derives the
// controller's deterministic data-disk PVC name.
func dataVolumeNameFor(inst *dbaasv1.DBInstance) string {
	if inst.Status.Resources.DataVolumeName != "" {
		return inst.Status.Resources.DataVolumeName
	}
	return harvester.DataVolumeName(inst.Name)
}

// ownerRefFor builds the controller owner reference the provider stamps on the
// same-namespace children it creates (VM, credentials/cloud-init Secrets). This
// is what makes the Owns() watches fire for them and lets GC back up the
// finalizer's explicit teardown.
func ownerRefFor(inst *dbaasv1.DBInstance) *metav1.OwnerReference {
	controller := true
	return &metav1.OwnerReference{
		APIVersion:         dbaasv1.GroupVersion.String(),
		Kind:               "DBInstance",
		Name:               inst.Name,
		UID:                inst.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &controller,
	}
}

// ensureVM asserts the VirtualMachine resource exists with the create-time shape.
// It observes the real VM through the manager client (KubeVirt types are
// registered in the scheme) rather than trusting status.Resources.VMName — so
// an out-of-band `kubectl delete vm` is observed as NotFound and repaired.
//
// Satisfied here means "the VM object exists", NOT "the VM booted": boot and
// PostgreSQL readiness belong to ensureDatabaseHealth. This step therefore never
// returns the timer flavor of Pending — its only Pending is the event-driven one
// right after a create.
func (r *vmStep) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	vmName := vmNameFor(inst)

	var vm kubevirtv1.VirtualMachine
	err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: vmName}, &vm)
	switch {
	case err == nil:
		// Observed == desired: the VM object exists. Re-record the ref for
		// instances whose status was lost/reset (self-heal of the ref itself).
		inst.Status.Resources.VMName = vmName
		inst.Status.Resources.DataVolumeName = dataVolumeNameFor(inst)
		inst.SetCurrentCondition(dbaasv1.ConditionVMReady, metav1.ConditionTrue,
			dbaasv1.ReasonVMPresent, "virtualmachine exists")
		return Satisfied()

	case apierrors.IsNotFound(err):
		return r.createVM(ctx, inst)

	default:
		return Transient(err)
	}
}

// createVM closes the observed gap: no VirtualMachine exists, so build one from
// spec + class via the Harvester client. CreatePostgresVM is idempotent (it
// ignores AlreadyExists on the VM), and the cloud-init Secret it references is
// rebuilt from durable Material every time this runs — so re-invoking it after
// a partial failure or an out-of-band VM delete is safe and reproduces the
// same VM.
func (r *vmStep) createVM(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	classSpec, ok := r.instanceClasses()[inst.Spec.DBInstanceClass]
	if !ok {
		// ensurePreflight validates this first; defensive so ensureVM alone can
		// never create a VM from an unknown class.
		msg := fmt.Sprintf("unknown dbInstanceClass %q", inst.Spec.DBInstanceClass)
		inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, dbaasv1.ReasonInvalidClass, msg)
		return Terminal(dbaasv1.ReasonInvalidClass, msg)
	}

	defaults := r.databaseDefaults()
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaults.MasterUsername
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	// Recomputed here rather than passed forward from preflight — ensureVM
	// doesn't trust state from other steps, matching how every other input
	// above is re-derived independently.
	entry, stream, ok := resolveBakedImage(defaults)
	if !ok {
		// ensurePreflight validates this first; defensive so ensureVM alone
		// can never create a VM against an unresolvable catalog stream.
		msg := fmt.Sprintf("OS stream %q is not available or not validated", defaults.OSVersion)
		inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, dbaasv1.ReasonOSImageInvalid, msg)
		return Terminal(dbaasv1.ReasonOSImageInvalid, msg)
	}
	engineVersion, ok := effectiveEngineVersion(inst.Spec.EngineVersion, entry)
	if !ok {
		// ensurePreflight validates this first; defensive so ensureVM alone
		// can never create a VM with a cloud-init that has no concrete
		// engine version for bootstrap.sh to activate.
		msg := fmt.Sprintf("engineVersion %q is not available in image revision %q (supported: %v)",
			inst.Spec.EngineVersion, stream.Revision, entry.SupportedEngineVersions)
		inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, dbaasv1.ReasonOSImageInvalid, msg)
		return Terminal(dbaasv1.ReasonOSImageInvalid, msg)
	}
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaults.StorageClass
	}

	dataVolumeName := dataVolumeNameFor(inst)
	inst.Status.Resources.DataVolumeName = dataVolumeName

	// Material was already resolved (and its three durable Secrets created)
	// by ensureCredentials earlier in the step order; this re-read is cheap.
	resolved, err := r.credentialsResolver().Resolve(ctx, inst)
	if err != nil {
		return Transient(err)
	}
	if resolved.Changed {
		msg := "credential material changed unexpectedly while preparing the VM; waiting for observation"
		return PendingAfter(dbaasv1.ReasonCredentialsCreated, msg, credentialRequeue)
	}
	userdata, networkdata := credentials.BuildCloudInit(credentials.BootstrapParams{
		ID:             inst.Name,
		DBName:         dbName,
		Port:           specPortWithDefault(inst.Spec.Port, defaults.Port),
		MasterUser:     masterUser,
		MaxConnections: classSpec.MaxConnections,
		BackupEnabled:  inst.Spec.BackupRetentionPeriod > 0,
		BackupWindow:   inst.Spec.PreferredBackupWindow,
		S3Config:       inst.Spec.S3BackupConfig,
		VMPassword:     inst.Spec.VMPassword,
		StaticNetwork:  inst.Spec.StaticNetwork,
		EngineVersion:  engineVersion,
	}, resolved.Material)

	cloudInitName := resource.CloudInitSecretName(inst)
	if _, err := resource.Apply(ctx, r.Client, r.Scheme(), inst, resource.CloudInitSecret{
		Instance:    inst,
		UserData:    userdata,
		NetworkData: networkdata,
	}); err != nil {
		return Transient(err)
	}
	inst.Status.Resources.CloudInitSecretName = cloudInitName

	vmName, err := r.Harvester.CreatePostgresVM(ctx, harvester.VMCreateParams{
		ID:                     inst.Name,
		Namespace:              inst.Namespace,
		CPUCores:               classSpec.CPUCores,
		MemoryMB:               classSpec.MemoryMB,
		OSImage:                entry.ImageName,
		DataVolumeRef:          dataVolumeName,
		DataVolumeSizeGB:       inst.Spec.AllocatedStorage,
		DataVolumeStorageClass: storageType,
		NADName:                inst.Spec.NetworkRef,
		MasterUser:             masterUser,
		Port:                   specPortWithDefault(inst.Spec.Port, defaults.Port),
		CloudInitSecretName:    cloudInitName,
		DNSServerIP:            inst.Spec.DNSServerIP,
		Owner:                  ownerRefFor(inst),
	})
	// Record the ref even on partial failure: the name is deterministic and
	// returned regardless of err, so persisting it lets the finalizer
	// (TeardownAll) clean up anything created before the error.
	inst.Status.Resources.VMName = vmName
	if err != nil {
		inst.SetCurrentCondition(dbaasv1.ConditionVMReady, metav1.ConditionFalse,
			dbaasv1.ReasonVMCreateFailed, err.Error())
		return Transient(err)
	}

	// Snapshot the immutable fields as applied; immutableDrift refuses later spec
	// changes that drift from this snapshot. The baked image itself is tracked
	// separately below (CurrentImageRevision), not here — it's expected to
	// change on every repave, unlike everything in AppliedSpec.
	inst.Status.AppliedSpec = &dbaasv1.AppliedSpec{
		NetworkRef:     inst.Spec.NetworkRef,
		DBName:         dbName,
		MasterUsername: masterUser,
		EngineVersion:  inst.Spec.EngineVersion,
		Port:           specPortWithDefault(inst.Spec.Port, defaults.Port),
		StorageType:    storageType,
		VMPassword:     inst.Spec.VMPassword,
		StaticNetwork:  inst.Spec.StaticNetwork.DeepCopy(),
	}
	inst.Status.CurrentImageRevision = stream.Revision
	inst.Status.Resources.OSDiskPVCName = fmt.Sprintf("pg-%s-os", inst.Name)
	inst.SetCurrentCondition(dbaasv1.ConditionVMReady, metav1.ConditionFalse,
		dbaasv1.ReasonVMCreated, "created virtualmachine, waiting for it to register")

	// Stop this pass so the next one observes the new VM before later steps run.
	// VM watch events and status events requeue reconciliation; VMI events cover boot progress.
	return Pending(dbaasv1.ReasonVMCreated, "created virtualmachine")
}
