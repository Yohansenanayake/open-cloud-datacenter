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
)

// DBSnapshotSpec requests a durable backup of a DBInstance, taken through
// Harvester (see yohan-docs/backups/harvester-vm-backup/). Creating a
// DBSnapshot always requests a manual snapshot; the controller creates
// automated ones itself and records that origin in status.
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
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
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
