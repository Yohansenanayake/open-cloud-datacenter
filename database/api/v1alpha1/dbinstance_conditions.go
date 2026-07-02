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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Status condition types for the bounded-reconcile status contract. Each ensure
// step owns one (or more) of these and stamps the condition's ObservedGeneration
// with inst.Generation when it writes it. ConditionDegraded and ConditionFailed
// are defined alongside the legacy constants in dbinstance_types.go.
const (
	ConditionAccepted         = "Accepted"
	ConditionPreflightReady   = "PreflightReady"
	ConditionCredentialsReady = "CredentialsReady"
	ConditionVMReady          = "VMReady"
	ConditionPowerStateReady  = "PowerStateReady"
	ConditionStorageReady     = "StorageReady"
	ConditionDatabaseReady    = "DatabaseReady"
	ConditionMonitoringReady  = "MonitoringReady"
	ConditionReady            = "Ready"
	ConditionCrashLoopHalted  = "CrashLoopHalted"
)

// SetCondition adds or updates a status condition. meta.SetStatusCondition only
// bumps LastTransitionTime when Status actually changes, which keeps the
// DeepEqual status-write-skip honest (no spurious writes on unchanged status).
func (s *DBInstanceStatus) SetCondition(c metav1.Condition) {
	meta.SetStatusCondition(&s.Conditions, c)
}

// GetCondition returns the condition of the given type, or nil if absent.
func (s *DBInstanceStatus) GetCondition(condType string) *metav1.Condition {
	return meta.FindStatusCondition(s.Conditions, condType)
}

// IsConditionTrue reports whether the named condition is present and True.
func (s *DBInstanceStatus) IsConditionTrue(condType string) bool {
	return meta.IsStatusConditionTrue(s.Conditions, condType)
}

// DerivePhase computes the RDS-style status.phase string from conditions and
// observed state. It is the single place phase is derived; phase is descriptive
// only and never gates control flow.
func DerivePhase(inst *DBInstance) string {
	s := &inst.Status
	switch {
	case !inst.DeletionTimestamp.IsZero():
		return StatusDeleting
	case s.IsConditionTrue(ConditionFailed) || s.IsConditionTrue(ConditionCrashLoopHalted):
		return StatusFailed
	case s.IsConditionTrue(ConditionReady):
		return StatusAvailable
	case inst.Spec.Running != nil && !*inst.Spec.Running:
		return StatusStopped
	case s.ObservedGeneration > 0 && s.ObservedGeneration < inst.Generation:
		return StatusModifying
	default:
		return StatusCreating
	}
}
