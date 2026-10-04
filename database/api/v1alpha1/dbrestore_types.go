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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DBRestoreSpec requests restoring a DBSnapshot into a new DBInstance
// (design §3/§6). A one-time operation request, not a durable resource —
// closer to a Job than to a DBInstance — so the whole spec is immutable:
// nothing in it means anything to change mid-flight.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable after creation"
type DBRestoreSpec struct {
	// SnapshotRef names the DBSnapshot to restore from, in the same
	// namespace. Must be a completed (Ready) snapshot; no automatic
	// latest-snapshot selection.
	// +required
	SnapshotRef corev1.LocalObjectReference `json:"snapshotRef"`

	// Mode selects the recovery mechanism. Snapshot (default) uses only the
	// snapshot's own captured local data and works after the source
	// instance is deleted. AvailableWAL is reserved for a future stage.
	// +optional
	// +kubebuilder:default=Snapshot
	// +kubebuilder:validation:Enum=Snapshot
	Mode string `json:"mode,omitempty"`

	// TargetInstanceName is the name of the DBInstance this restore
	// creates. Deliberately independent of this object's own name: a retry
	// after a failed attempt creates a new DBRestore object and can still
	// target the same instance name.
	// +required
	TargetInstanceName string `json:"targetInstanceName"`

	// DBInstanceClass is the target instance's compute class —
	// user-specified, same meaning as DBInstanceSpec.DBInstanceClass.
	// +required
	// +kubebuilder:validation:MinLength=1
	DBInstanceClass string `json:"dbInstanceClass"`

	// NetworkRef is the target instance's network — user-specified, never
	// inherited from the source (design §2.4).
	// +required
	NetworkRef string `json:"networkRef"`

	// StaticNetwork is the target instance's static IP config, if any —
	// user-specified; never copies the source's static IP.
	// +optional
	StaticNetwork *NetworkConfig `json:"staticNetwork,omitempty"`

	// AllocatedStorage must be at least the snapshot's recorded data-volume
	// size (§2.4) — smaller is rejected, larger is accepted and passed
	// through to the restore PVC's request.
	// +required
	// +kubebuilder:validation:Minimum=1
	AllocatedStorage int `json:"allocatedStorage"`

	// Backup opts the target instance into backup capability, independent
	// of the source. Omission means no ongoing backup capability.
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`
}

const (
	// DBRestoreSpec.Mode values.
	RestoreModeSnapshot = "Snapshot"

	// LabelDBRestoreUID marks the restore PVC with the UID of the DBRestore
	// that created it. The PVC deliberately has no owner reference (it
	// outlives its DBRestore), so this label is how every later pass
	// re-verifies that a PVC under the expected name is really ours.
	LabelDBRestoreUID = "dbaas.opencloud.wso2.com/restore-uid"
)

// ResolvedRestoreFields is the §2.4 field-inheritance outcome — always
// silently inherited from the snapshot (never a DBRestore.spec field),
// captured once and exposed here for observability.
type ResolvedRestoreFields struct {
	// +optional
	DBName string `json:"dbName,omitempty"`
	// +optional
	MasterUsername string `json:"masterUsername,omitempty"`
	// +optional
	EngineVersion string `json:"engineVersion,omitempty"`
	// +optional
	Port int `json:"port,omitempty"`
	// +optional
	StorageType string `json:"storageType,omitempty"`
}

// DBRestoreStatus is the observed state of a DBRestore.
type DBRestoreStatus struct {
	// Stage is the controller-observed restore progress (§5.1), recomputed
	// from live cluster state every pass. Non-terminal values are output
	// only — reconcile logic never branches on them. Succeeded/Failed mark
	// the end of this one-time operation (like a Job's completion) and stop
	// further convergence, but never by themselves justify a destructive
	// action: deletion re-checks the target's live readiness too.
	// +optional
	Stage string `json:"stage,omitempty"`

	// Reason is a stable, machine-readable explanation for the current
	// stage, particularly Failed.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable detail for Reason.
	// +optional
	Message string `json:"message,omitempty"`

	// SnapshotUID is the UID of the DBSnapshot spec.snapshotRef resolved to,
	// captured once at admission so a later rename or recreation of the
	// same-named snapshot can never silently redirect an in-progress
	// restore.
	// +optional
	SnapshotUID string `json:"snapshotUID,omitempty"`

	// SourceInstanceUID mirrors the snapshot's own status.source.instanceUID
	// — never a live lookup of the source DBInstance, which is what lets
	// this be captured (and the restore-hold Lease labeled with it) even
	// after the source is deleted.
	// +optional
	SourceInstanceUID types.UID `json:"sourceInstanceUID,omitempty"`

	// DataVolumeSnapshotName mirrors the snapshot's own recorded value —
	// the restore PVC's spec.dataSource.
	// +optional
	DataVolumeSnapshotName string `json:"dataVolumeSnapshotName,omitempty"`

	// Resolved is captured once, alongside the fields above.
	// +optional
	Resolved *ResolvedRestoreFields `json:"resolved,omitempty"`

	// TargetInstanceUID is the UID of the DBInstance this restore created,
	// recorded once and never cleared — not even after that instance is
	// gone. It only ever restricts what the controller does: once set, a
	// missing target means "lost", never "not created yet", so it is never
	// recreated. Liveness and readiness are always re-observed from the
	// cluster, never inferred from this field.
	// +optional
	TargetInstanceUID types.UID `json:"targetInstanceUID,omitempty"`

	// ObservedGeneration tracks which spec version has been reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

const (
	// DBRestoreStatus.Stage values.
	RestoreStagePreparing        = "Preparing"
	RestoreStageRestoringVolume  = "RestoringVolume"
	RestoreStageStartingDatabase = "StartingDatabase"
	RestoreStageSucceeded        = "Succeeded"
	RestoreStageFailed           = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dbrestore
// +kubebuilder:printcolumn:name="Snapshot",type=string,JSONPath=`.spec.snapshotRef.name`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetInstanceName`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.status.stage`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DBRestore represents one restore operation: a snapshot plus target-instance
// parameters in, a new DBInstance out. Namespaced. A one-time operation
// request — see the package doc comment on DBRestoreSpec.
type DBRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DBRestoreSpec   `json:"spec,omitempty"`
	Status DBRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DBRestoreList contains a list of DBRestore.
type DBRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DBRestore{}, &DBRestoreList{})
}
