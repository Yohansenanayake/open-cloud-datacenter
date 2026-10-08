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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DBSnapshotSpec requests a durable Harvester backup of a DBInstance.
// User-created snapshots are manual; the scheduler creates automated snapshots
// and records their origin in status.
type DBSnapshotSpec struct {
	// SourceInstanceRef names the DBInstance to snapshot, in the same
	// namespace. Immutable after creation.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="sourceInstanceRef is immutable after creation"
	SourceInstanceRef corev1.LocalObjectReference `json:"sourceInstanceRef"`
}

// DBSnapshotStatus is the observed state of a DBSnapshot.
type DBSnapshotStatus struct {
	// Origin distinguishes a user-requested snapshot from one the
	// controller created on the automated schedule. Recorded once at
	// creation; never changes afterward.
	// +optional
	Origin string `json:"origin,omitempty"`

	// Conditions for this snapshot's backend backup.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration tracks which spec version has been reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Source records the database settings at snapshot admission and is not
	// updated afterward. Restore uses this metadata instead of the live source,
	// so later source changes or deletion do not affect the recorded settings.
	// +optional
	Source *SourceMetadata `json:"source,omitempty"`

	// DataVolumeSnapshotName identifies the PostgreSQL data-volume snapshot
	// within the VM backup. Captured when the backup is ready, it becomes the
	// restore PVC data source without requiring a live source instance.
	// +optional
	DataVolumeSnapshotName string `json:"dataVolumeSnapshotName,omitempty"`

	// Phase summarizes the Ready condition for display: Queued, InProgress,
	// Ready, Failed, or Deleting. Reconciliation uses conditions directly.
	// +optional
	Phase string `json:"phase,omitempty"`

	// StartTime is when the backend backup was created — time spent queued
	// for a backup slot doesn't count. Set once.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the snapshot was first observed finished:
	// Ready, failed, timed out or rejected. Set once.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Progress is the backend backup's progress, 0-100, as Harvester reports
	// it for the whole VM backup (OS and data disks together).
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Progress int32 `json:"progress,omitempty"`
}

const (
	// DBSnapshotStatus.Phase values.
	SnapshotPhaseQueued     = "Queued"
	SnapshotPhaseInProgress = "InProgress"
	SnapshotPhaseReady      = "Ready"
	SnapshotPhaseFailed     = "Failed"
	SnapshotPhaseDeleting   = "Deleting"
)

// SourceMetadata is the source DBInstance's restore-inheritance data,
// recorded on the DBSnapshot that resulted from it.
type SourceMetadata struct {
	// InstanceUID is the source DBInstance's UID at admission time. The
	// restore-hold Lease is labeled with it — spec.sourceInstanceRef is a
	// name, and the source may no longer exist by restore time.
	// +optional
	InstanceUID types.UID `json:"instanceUID,omitempty"`

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
	// AllocatedStorage is the source's size at the moment of this snapshot,
	// not necessarily its current size — allocatedStorage is mutable, and a
	// restore must match what was actually backed up.
	// +optional
	AllocatedStorage int `json:"allocatedStorage,omitempty"`
	// ImageRevision is the baked-image catalog revision
	// (DBInstance.status.currentImageRevision) the source was running at
	// snapshot time.
	// +optional
	ImageRevision string `json:"imageRevision,omitempty"`

	// DBInstanceClass, NetworkRef and Backup record source settings as UI
	// hints. Restore requires its own class and network; backup is opt-in.
	// The source's static IP is omitted to avoid address reuse.
	// +optional
	DBInstanceClass string `json:"dbInstanceClass,omitempty"`
	// +optional
	NetworkRef string `json:"networkRef,omitempty"`
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`
}

const (
	// DBSnapshotStatus.Origin values.
	SnapshotOriginManual    = "Manual"
	SnapshotOriginAutomated = "Automated"

	// LabelSnapshotOrigin is set by the DBInstance scheduler on every
	// DBSnapshot it creates, so DBSnapshotReconciler can record the right
	// Status.Origin without duplicating the scheduling decision. Absent on a
	// user-created (Manual) DBSnapshot.
	LabelSnapshotOrigin = "dbaas.opencloud.wso2.com/snapshot-origin"
)

// SetCondition adds or updates a status condition. meta.SetStatusCondition
// only bumps LastTransitionTime when Status actually changes.
func (s *DBSnapshotStatus) SetCondition(c metav1.Condition) {
	meta.SetStatusCondition(&s.Conditions, c)
}

// GetCondition returns the condition of the given type, or nil if absent.
func (s *DBSnapshotStatus) GetCondition(condType string) *metav1.Condition {
	return meta.FindStatusCondition(s.Conditions, condType)
}

// IsConditionTrue reports whether the named condition is present and True.
func (s *DBSnapshotStatus) IsConditionTrue(condType string) bool {
	return meta.IsStatusConditionTrue(s.Conditions, condType)
}

// GetConditions exposes DBSnapshot conditions through the shared condition
// patching contract.
func (in *DBSnapshot) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

// SetConditions replaces DBSnapshot conditions through the shared condition
// patching contract.
func (in *DBSnapshot) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dbsnap
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.sourceInstanceRef.name`
// +kubebuilder:printcolumn:name="Origin",type=string,JSONPath=`.status.origin`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].reason`
// +kubebuilder:printcolumn:name="Progress",type=integer,JSONPath=`.status.progress`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DBSnapshot represents one durable backup of a DBInstance. Namespaced —
// lives alongside its source DBInstance.
type DBSnapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DBSnapshotSpec   `json:"spec,omitempty"`
	Status DBSnapshotStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DBSnapshotList contains a list of DBSnapshot.
type DBSnapshotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBSnapshot `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DBSnapshot{}, &DBSnapshotList{})
}
