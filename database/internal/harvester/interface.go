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

package harvester

import (
	"context"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ClientInterface is the controller-facing Harvester contract. It intentionally
// hides whether Harvester resources are managed through the dynamic client or
// future typed clients.
type ClientInterface interface {
	CreateDataVolume(ctx context.Context, id, ns string, sizeGB int, storageClass string) (string, error)
	ResizeDataVolume(ctx context.Context, ns, vmName, dvName string, newSizeGB int) error

	CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName, credSecretName, cloudInitSecretName, caCertPEM string, err error)
	GetVMIReadiness(ctx context.Context, ns, vmName string) (VMIReadiness, error)
	StopVM(ctx context.Context, ns, vmName string) error
	StartVM(ctx context.Context, ns, vmName string) error
	ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error

	DeleteSecret(ctx context.Context, ns, name string) error
	// RemoveCloudInitDisk patches the VM spec to remove the cloudinit disk and
	// volume so future VMI restarts don't try to mount the cloud-init secret.
	// Must be called before DeleteSecret on the cloud-init secret; otherwise
	// poweroff/restart leaves the VM stuck in Starting with FailedMount.
	RemoveCloudInitDisk(ctx context.Context, ns, vmName string) error
	// PrepareCloudInitForRepave recreates the ephemeral cloud-init Secret from
	// the existing credentials Secret and reattaches the cloudinit disk to a
	// halted VM before an OS-disk repave boot.
	PrepareCloudInitForRepave(ctx context.Context, p VMCreateParams, vmName, credSecretName, cloudInitSecretName string) error

	DeployMonitoring(ctx context.Context, id, ns, vmIP string) (svcName, smName, grafanaURL, promTarget string, err error)
	TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) error

	// Repave helpers — used by phaseRepave() to swap the OS disk.
	// ClearDataVolumeOwnerRef removes all ownerReferences from a DataVolume so
	// it is not cascade-deleted when the VM CR is patched during repave.
	ClearDataVolumeOwnerRef(ctx context.Context, ns, dvName string) error
	// DeleteDataVolume deletes a DataVolume by name. NotFound is treated as success.
	DeleteDataVolume(ctx context.Context, ns, dvName string) error
	// DeletePVC deletes a PersistentVolumeClaim by name. NotFound is treated as
	// success. Needed because Harvester creates disk PVCs (from the VM's
	// volumeClaimTemplates annotation) without ownerReferences, so nothing
	// cascade-deletes them — explicit deletion is the only way they go away.
	DeletePVC(ctx context.Context, ns, name string) error
	// SwapVMOSDisk points the VM's OS disk at a fresh, revision-suffixed disk
	// (pg-<id>-os-<rev>) provisioned from the image referenced by imgRef (name
	// or displayName in namespace default). The storageClass is resolved from
	// the image's own status.storageClassName, so this works whether the image
	// was uploaded via kubectl (metadata.name matches) or the Harvester UI
	// (auto-generated name, displayName matches). It returns the name of the
	// disk it replaced ("" when the VM is already on the target disk) so the
	// caller can delete the old disk — the two names never collide, which is
	// what makes the swap race-free.
	SwapVMOSDisk(ctx context.Context, ns, vmName, instID, imgRef string) (oldDiskName string, err error)
}
