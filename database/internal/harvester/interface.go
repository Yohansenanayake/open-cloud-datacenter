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
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ClientInterface is the controller-facing Harvester contract. It intentionally
// hides whether Harvester resources are managed through the dynamic client or
// future typed clients.
type ClientInterface interface {
	ResizeDataVolume(ctx context.Context, ns, vmName, dvName string, newSizeGB int) error
	ResolveVMImage(ctx context.Context, ref string) (ResolvedVMImage, error)

	// CreatePostgresVM creates the VM from an already-provisioned cloud-init
	// Secret (p.CloudInitSecretName) — credential/TLS material and the
	// cloud-init payload are resolved by internal/credentials and applied by
	// internal/resource before this is called; the provider only builds VM
	// shape.
	CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName string, err error)
	GetVMIReadiness(ctx context.Context, ns, vmName string) (VMIReadiness, error)
	StopVM(ctx context.Context, ns, vmName string) error
	StopVMForCrashLoop(ctx context.Context, ns, vmName, haltedVMIUID string) error
	ClearCrashLoopHalt(ctx context.Context, ns, vmName string) error
	StartVM(ctx context.Context, ns, vmName string) error
	ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error

	// Monitoring objects (metrics Service/Endpoints, ServiceMonitor) are
	// builder-managed by the controller (internal/resource) — no provider
	// method. TeardownAll still deletes them by ref as the finalizer's
	// authoritative cleanup; owner-ref GC is the backup.
	TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) error

	// SwapVMOSDisk repoints the "os-disk" volumeClaimTemplates entry to a new
	// revision-suffixed PVC backed by newImageRef's StorageClass, and repoints
	// the "os-disk" volume's claimName to match. Returns (oldPVCName,
	// newPVCName, err); oldPVCName is "" if the VM was already on the target
	// disk (idempotent re-entry).
	SwapVMOSDisk(ctx context.Context, ns, vmName, instID, newImageRef string) (oldPVCName, newPVCName string, err error)

	// DeletePVC deletes a PVC by name. Idempotent; NotFound is success.
	DeletePVC(ctx context.Context, ns, name string) error

	// DeletePVCWithUID deletes a PVC only if it is still the object with
	// uid — for a caller that has just verified that object is its own.
	// NotFound is success; a replaced object is a Conflict error.
	DeletePVCWithUID(ctx context.Context, ns, name string, uid types.UID) error

	// MarkVMPVCsForRemoval reads the live VM and lists every PVC it mounts
	// that owned accepts in Harvester's removedPersistentVolumeClaims
	// annotation, so Harvester's VM finalizer deletes them with the VM.
	// found is false when the VM no longer exists.
	MarkVMPVCsForRemoval(ctx context.Context, ns, vmName string, owned func(pvcName string) bool) (found bool, err error)

	// GetVMOSDiskImageID returns the Harvester ImageID ("namespace/name")
	// recorded on the VM's current OS-disk PVC — ground truth for which
	// baked image is actually running, used to self-heal
	// DBInstance.Status.CurrentImageRevision independent of whether a prior
	// status write persisted. Returns ("", nil) if not yet determinable.
	GetVMOSDiskImageID(ctx context.Context, ns, vmName string) (string, error)

	// GetVMOSDiskPVCName returns the claimName of the VM's current "os-disk"
	// volume — ground truth for which PVC actually backs it, used to
	// self-heal DBInstance.Status.Resources.OSDiskPVCName the same way
	// GetVMOSDiskImageID self-heals CurrentImageRevision. Unlike the data
	// disk's PVC name, this can't be recomputed deterministically once a
	// repave has happened (it becomes revision-suffixed), so it must be
	// observed from the live VM rather than derived. Returns ("", nil) if
	// not yet determinable.
	GetVMOSDiskPVCName(ctx context.Context, ns, vmName string) (string, error)

	// ResolveVMImageDisplayName returns the DisplayName of the
	// VirtualMachineImage identified by ns/name — the inverse of
	// ResolveVMImage's own displayName-fallback lookup. GetVMOSDiskImageID
	// observes a real Harvester object identity off the VM's live os-disk
	// PVC annotation, but internal/catalog is keyed by the human-readable
	// strings operators write into BakedImages; on a real cluster, imported
	// images commonly get an auto-generated object name with that string
	// only on DisplayName, so self-heal must translate back through this
	// before comparing against the catalog. Returns ("", nil), not an
	// error, if the image no longer exists.
	ResolveVMImageDisplayName(ctx context.Context, ns, name string) (string, error)

	// CreateVMBackup requests a durable Harvester backup of sourceVMName.
	// Always spec.type: Backup (durable, uploaded externally) — Snapshot is
	// local-only and must never be used for this design. Idempotent:
	// AlreadyExists is treated as success.
	CreateVMBackup(ctx context.Context, ns, name, sourceVMName string, owner *metav1.OwnerReference) error

	// GetVMBackupStatus returns the translated status of a VirtualMachineBackup.
	// dataVolumePVCName identifies which of the backup's volumes is the
	// PostgreSQL data volume — a VirtualMachineBackup covers every volume on
	// the source VM, but DBaaS restore only ever needs this one.
	GetVMBackupStatus(ctx context.Context, ns, name, dataVolumePVCName string) (VMBackupStatus, error)

	// DeleteVMBackup deletes a VirtualMachineBackup by name. Idempotent;
	// NotFound is success.
	DeleteVMBackup(ctx context.Context, ns, name string) error

	// CreateRestorePVC restores a PVC from a VolumeSnapshot in the same namespace.
	// It does not wait for binding. AlreadyExists is treated as success; callers
	// must verify the existing PVC labels and dataSource with GetPVC.
	CreateRestorePVC(ctx context.Context, ns, pvcName, volumeSnapshotName string, sizeGB int, storageClassName string, labels map[string]string) error

	// GetPVC returns the live PVC (NotFound as an error). Creating a restore
	// PVC is instant, but Longhorn copying the snapshot's data into it is
	// not — callers wait for Bound, and verify the PVC's identity, on every
	// pass rather than trusting an earlier observation.
	GetPVC(ctx context.Context, ns, name string) (*corev1.PersistentVolumeClaim, error)

	// GetVolumeSnapshotState returns the live state of a CSI VolumeSnapshot
	// — the object a restore PVC's dataSource actually reads from. A
	// DBSnapshot's Ready condition is only a record of what was true when
	// its backup completed; this is the thing itself. NotFound is returned
	// as an error.
	GetVolumeSnapshotState(ctx context.Context, ns, name string) (VolumeSnapshotState, error)
}

// VolumeSnapshotState is the provider-neutral live state of a VolumeSnapshot.
type VolumeSnapshotState struct {
	ReadyToUse bool
	// Deleting is true once the VolumeSnapshot has a deletionTimestamp.
	Deleting bool
	// ErrorMessage is empty unless the snapshotter recorded an error.
	ErrorMessage string
}

// VMBackupStatus is the provider-neutral status of a VirtualMachineBackup.
// Harvester API types stay behind ClientInterface.
type VMBackupStatus struct {
	// CreatedAt is the VirtualMachineBackup's creationTimestamp — set by the
	// API server, so a backup's age can always be re-checked.
	CreatedAt  time.Time
	ReadyToUse bool
	// Progress is the whole backup's progress, 0-100, as Harvester reports
	// it (all of the VM's volumes together).
	Progress int
	// ErrorMessage is empty unless Harvester recorded a backup failure.
	ErrorMessage string
	// DataVolumeSnapshotName is the VolumeSnapshot object name backing the
	// PostgreSQL data volume specifically, empty if the backup isn't ready
	// yet or the requested PVC name isn't among its volumes.
	DataVolumeSnapshotName string
}

// ResolvedVMImage contains the provider-neutral image fields needed to build a
// VM. Harvester API types stay behind ClientInterface.
type ResolvedVMImage struct {
	Namespace        string
	Name             string
	StorageClassName string
}

// Semantic image-resolution failures let the controller choose terminal or
// pending reconciliation without inspecting provider error strings.
var (
	ErrVMImageReferenceInvalid = errors.New("invalid VM image reference")
	ErrVMImageNotFound         = errors.New("VM image not found")
	ErrVMImageNotReady         = errors.New("VM image not ready")
	ErrVMImageAmbiguous        = errors.New("ambiguous VM image reference")
)

// VMCreateParams bundles everything needed to create a PostgreSQL VM. Fields
// used only for credential/cloud-init generation (DBName, MaxConnections,
// backup, VMPassword, StaticNetwork) live in internal/credentials'
// BootstrapParams instead — the provider only builds VM shape and consumes an
// already-provisioned cloud-init Secret. Port and MasterUser stay: the VMI
// readiness probe embeds them directly (see buildPostgresVM).
type VMCreateParams struct {
	ID                     string
	Namespace              string
	CPUCores               int
	MemoryMB               int
	OSImage                string
	OSDiskPVCName          string
	DataVolumeRef          string
	DataVolumeSizeGB       int
	DataVolumeStorageClass string
	NADName                string
	MasterUser             string
	Port                   int
	// CloudInitSecretName is the pre-created ephemeral Secret (userdata +
	// networkdata) this VM's cloudInitNoCloud volume references.
	CloudInitSecretName string
	// Owner, when non-nil, is stamped as the controller owner reference on the
	// VM this call creates, so Owns() watches fire and GC backs up the
	// finalizer teardown.
	Owner *metav1.OwnerReference
}

// VMIReadiness bundles the VMI state fields needed for phase gating and
// liveness monitoring from a single VMI fetch.
//
//   - Running: VMI phase is Running.
//   - IP: data-network IP reported by the guest agent; empty until QGA populates it.
//   - Ready: VMI readiness condition is True (readiness probe has passed).
//   - AgentConnected: QGA virtio channel is active.
//   - VMIUID: VMI object UID; a change across reconciles indicates an unplanned restart.
type VMIReadiness struct {
	Running        bool
	IP             string
	Ready          bool
	AgentConnected bool
	VMIUID         string
}
