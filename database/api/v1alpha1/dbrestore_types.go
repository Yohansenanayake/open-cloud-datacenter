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

// DBRestoreSpec restores a ready DBSnapshot into a new DBInstance.
// The spec is immutable; retry a failed operation with a new DBRestore.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable after creation"
type DBRestoreSpec struct {
	// SnapshotRef names the DBSnapshot to restore from, in the same
	// namespace. Must be a completed (Ready) snapshot; no automatic
	// latest-snapshot selection.
	// +required
	// +kubebuilder:validation:XValidation:rule="self.name != ''",message="snapshotRef.name must not be empty"
	SnapshotRef corev1.LocalObjectReference `json:"snapshotRef"`

	// Mode selects the recovery mechanism. Only Snapshot is supported.
	// It uses the snapshot data and works after the source instance is deleted.
	// +optional
	// +kubebuilder:default=Snapshot
	// +kubebuilder:validation:Enum=Snapshot
	Mode string `json:"mode,omitempty"`

	// TargetInstanceName names the new DBInstance. It must be a DNS label
	// of at most 52 characters and may differ from the DBRestore name.
	// +required
	// +kubebuilder:validation:MaxLength=52
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	TargetInstanceName string `json:"targetInstanceName"`

	// DBInstanceClass is the target instance's compute class —
	// user-specified, same meaning as DBInstanceSpec.DBInstanceClass.
	// +required
	// +kubebuilder:validation:MinLength=1
	DBInstanceClass string `json:"dbInstanceClass"`

	// NetworkRef is the target instance's network, supplied by the caller.
	// It is not inherited from the source.
	// +required
	// +kubebuilder:validation:MinLength=1
	NetworkRef string `json:"networkRef"`

	// StaticNetwork is the target instance's static IP config, if any —
	// user-specified; never copies the source's static IP.
	// +optional
	StaticNetwork *NetworkConfig `json:"staticNetwork,omitempty"`

	// AllocatedStorage is the target data-volume size in GiB. It must be
	// at least the snapshot's recorded size; larger values expand the restore PVC.
	// +required
	// +kubebuilder:validation:Minimum=1
	AllocatedStorage int `json:"allocatedStorage"`

	// Backup opts the target instance into backup capability, independent
	// of the source. Omission means no ongoing backup capability.
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`

	// VMPassword is passed through to the target's spec.vmPassword: the
	// console/SSH password for the VM's default user (ubuntu). For
	// development and debugging only — leave empty in production.
	// +optional
	VMPassword string `json:"vmPassword,omitempty"`
}

const (
	// DBRestoreSpec.Mode values.
	RestoreModeSnapshot = "Snapshot"

	// LabelDBRestoreUID identifies the restore that created a PVC. The PVC
	// outlives the DBRestore, so ownership is verified through this label
	// instead of an owner reference.
	LabelDBRestoreUID = "dbaas.opencloud.wso2.com/restore-uid"
)

// ResolvedRestoreFields records the database settings inherited from the
// snapshot at admission. These settings cannot be overridden in DBRestoreSpec.
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
	// Stage reports progress observed from live cluster state. Succeeded and
	// Failed end reconciliation. Deletion also checks live target readiness
	// to protect a database that became ready before its status was recorded.
	// +optional
	Stage string `json:"stage,omitempty"`

	// Reason is a stable, machine-readable explanation for the current
	// stage, particularly Failed.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable detail for Reason.
	// +optional
	Message string `json:"message,omitempty"`

	// SnapshotUID records the snapshot identity at admission, preventing
	// a recreated snapshot with the same name from redirecting the restore.
	// +optional
	SnapshotUID string `json:"snapshotUID,omitempty"`

	// SourceInstanceUID is copied from the snapshot's source metadata. It
	// identifies the source for restore holds even after the source is deleted.
	// +optional
	SourceInstanceUID types.UID `json:"sourceInstanceUID,omitempty"`

	// SourceInstanceName is the snapshot's spec.sourceInstanceRef.name,
	// captured alongside SourceInstanceUID — provenance for the target.
	// +optional
	SourceInstanceName string `json:"sourceInstanceName,omitempty"`

	// DataVolumeSnapshotName mirrors the snapshot's own recorded value —
	// the restore PVC's spec.dataSource.
	// +optional
	DataVolumeSnapshotName string `json:"dataVolumeSnapshotName,omitempty"`

	// Resolved is captured once, alongside the fields above.
	// +optional
	Resolved *ResolvedRestoreFields `json:"resolved,omitempty"`

	// TargetInstanceUID records the created database identity and is never
	// cleared. If that instance disappears, the restore fails instead of
	// recreating it. Readiness is checked from live state.
	// +optional
	TargetInstanceUID types.UID `json:"targetInstanceUID,omitempty"`

	// Deadline is the creation time plus the operator's restore timeout.
	// It is informational; the controller recomputes it on each pass.
	// +optional
	Deadline *metav1.Time `json:"deadline,omitempty"`

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
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DBRestore is a namespaced request to restore a snapshot into a new DBInstance.
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
