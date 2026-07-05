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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// vmNameFor returns the deterministic VM name for an instance. It matches the
// name CreatePostgresVM derives ("pg-<id>"), preferring the recorded ref for
// instances created before the convention existed.
func vmNameFor(inst *dbaasv1.DBInstance) string {
	if inst.Status.Resources.VMName != "" {
		return inst.Status.Resources.VMName
	}
	return fmt.Sprintf("pg-%s", inst.Name)
}

// ensureVM asserts the VirtualMachine resource exists with the create-time shape.
// It observes the real VM through the manager client (KubeVirt types are in the
// scheme since PR1) rather than trusting status.Resources.VMName — so an
// out-of-band `kubectl delete vm` is observed as NotFound and repaired.
//
// Satisfied here means "the VM object exists", NOT "the VM booted": boot and
// PostgreSQL readiness belong to ensureDatabaseHealth. This step therefore never
// returns the timer flavor of Pending — its only Pending is the event-driven one
// right after a create.
func (r *DBInstanceReconciler) ensureVM(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	vmName := vmNameFor(inst)

	var vm kubevirtv1.VirtualMachine
	err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: vmName}, &vm)
	switch {
	case err == nil:
		// Observed == desired: the VM object exists. Re-record the ref for
		// instances whose status was lost/reset (self-heal of the ref itself).
		inst.Status.Resources.VMName = vmName
		setStepCond(inst, dbaasv1.ConditionVMReady, metav1.ConditionTrue,
			"VMPresent", "virtualmachine exists")
		return satisfied()

	case apierrors.IsNotFound(err):
		return r.createVM(ctx, inst)

	default:
		return transient(err)
	}
}

// createVM closes the observed gap: no VirtualMachine exists, so build one from
// spec + class via the Harvester client. CreatePostgresVM is idempotent — it
// ignores AlreadyExists on the VM, reuses existing credential material, and
// overwrites the not-yet-consumed cloud-init Secret — so re-invoking it after a
// partial failure or an out-of-band VM delete is safe and preserves credentials.
func (r *DBInstanceReconciler) createVM(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	classSpec, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]
	if !ok {
		// ensurePreflight validates this first; defensive so ensureVM alone can
		// never create a VM from an unknown class.
		msg := fmt.Sprintf("unknown dbInstanceClass %q", inst.Spec.DBInstanceClass)
		markProvisioningFailed(inst, "InvalidClass", msg)
		return terminal("InvalidClass", msg)
	}

	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaultMasterUser
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	osImage := inst.Spec.OSImage
	if osImage == "" {
		osImage = defaultOSImage
	}
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaultStorageType
	}

	// Reserve the deterministic data-disk name. The PVC itself is created by
	// Harvester from the VM's volumeClaimTemplates annotation, so there is no
	// DataVolume object to observe — the recorded ref is the persisted fact
	// (allowed: it cannot be reconstructed by observation).
	if inst.Status.Resources.DataVolumeName == "" {
		dvName, err := r.Harvester.CreateDataVolume(ctx, inst.Name, inst.Namespace, inst.Spec.AllocatedStorage, storageType)
		if err != nil {
			return transient(err)
		}
		inst.Status.Resources.DataVolumeName = dvName
	}

	vmName, credSecretName, cloudInitSecretName, caCertPEM, err := r.Harvester.CreatePostgresVM(ctx, harvester.VMCreateParams{
		ID:                     inst.Name,
		Namespace:              inst.Namespace,
		CPUCores:               classSpec.CPUCores,
		MemoryMB:               classSpec.MemoryMB,
		OSImage:                osImage,
		DataVolumeRef:          inst.Status.Resources.DataVolumeName,
		DataVolumeSizeGB:       inst.Spec.AllocatedStorage,
		DataVolumeStorageClass: storageType,
		NADName:                inst.Spec.NetworkRef,
		MasterUser:             masterUser,
		DBName:                 dbName,
		Port:                   specPort(inst.Spec.Port),
		MaxConnections:         classSpec.MaxConnections,
		BackupEnabled:          inst.Spec.BackupRetentionPeriod > 0,
		BackupWindow:           inst.Spec.PreferredBackupWindow,
		S3Config:               inst.Spec.S3BackupConfig,
		VMPassword:             inst.Spec.VMPassword,
		StaticNetwork:          inst.Spec.StaticNetwork,
		DNSServerIP:            inst.Spec.DNSServerIP,
	})
	// Record resource refs even on partial failure: the names are deterministic
	// and returned regardless of err, so persisting them lets the finalizer
	// (TeardownAll) clean up anything created before the error instead of leaking it.
	inst.Status.Resources.VMName = vmName
	inst.Status.Resources.SecretName = credSecretName
	inst.Status.Resources.CloudInitSecretName = cloudInitSecretName
	if err != nil {
		setStepCond(inst, dbaasv1.ConditionVMReady, metav1.ConditionFalse,
			"VMCreateFailed", err.Error())
		return transient(err)
	}

	inst.Status.CACertPEM = caCertPEM
	inst.Status.MasterUserSecret = &dbaasv1.MasterUserSecretRef{
		Name:   credSecretName,
		Status: dbaasv1.SecretStatusActive,
	}
	// Snapshot the immutable fields as applied; immutableDrift refuses later spec
	// changes that drift from this snapshot.
	inst.Status.AppliedSpec = &dbaasv1.AppliedSpec{
		NetworkRef:     inst.Spec.NetworkRef,
		OSImage:        osImage,
		DBName:         dbName,
		MasterUsername: masterUser,
		EngineVersion:  inst.Spec.EngineVersion,
		Port:           specPort(inst.Spec.Port),
		StorageType:    storageType,
	}
	setStepCond(inst, dbaasv1.ConditionVMReady, metav1.ConditionFalse,
		"VMCreated", "created virtualmachine, waiting for it to register")
	inst.Status.Message = "VM created, waiting for PostgreSQL to initialize"

	// Event-driven Pending: this pass changed status (Resources/conditions), so the
	// status patch re-triggers a reconcile immediately; the VMI watch covers boot
	// progress after that. No timer needed.
	return pending("VMCreated", "created virtualmachine")
}
