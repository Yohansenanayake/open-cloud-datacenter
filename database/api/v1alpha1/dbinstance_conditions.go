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

// ConditionReason is a stable, machine-readable reason used by status
// conditions. Human-readable detail belongs in Condition.Message.
type ConditionReason string

// Status condition types for the bounded-reconcile status contract. Each ensure
// step owns one (or more) of these and stamps the condition's ObservedGeneration
// with inst.Generation when it writes it.
const (
	ConditionAccepted              = "Accepted"
	ConditionPreflightReady        = "PreflightReady"
	ConditionCredentialsReady      = "CredentialsReady"
	ConditionVMReady               = "VMReady"
	ConditionPowerStateReady       = "PowerStateReady"
	ConditionStorageReady          = "StorageReady"
	ConditionStorageChangeRejected = "StorageChangeRejected"
	ConditionResizeInProgress      = "ResizeInProgress"
	// ConditionDatabaseReady is the narrow, single-purpose signal for "is
	// Postgres itself reachable right now" — set by whichever step actually
	// takes the VM down (ensureDatabaseHealth, ensureResize, ensurePowerState),
	// probed via the VMI readiness gate elsewhere.
	ConditionDatabaseReady   = "DatabaseReady"
	ConditionMonitoringReady = "MonitoringReady"
	// ConditionReady is the aggregate product-stack condition external tooling
	// conventionally looks for by name (kubectl wait --for=condition=Ready,
	// kstatus-style dashboards). It requires both PostgreSQL reachability and
	// monitoring-resource convergence; ConditionDatabaseReady remains the narrow
	// signal for clients interested specifically in PostgreSQL usability.
	ConditionReady                = "Ready"
	ConditionInterventionRequired = "InterventionRequired"
	ConditionCrashLoopHalted      = "CrashLoopHalted"
	// ConditionDegraded is report-only: the instance is provisioned and supposed
	// to serve, but readiness or guest-agent attribution says it is unhealthy.
	ConditionDegraded        = "Degraded"
	ConditionDeletionBlocked = "DeletionBlocked"
)

// Find a condition witg more reasons
// Condition reason constants used in Conditions[].Reason and related events.
const (
	ReasonFinalizerAdded              ConditionReason = "FinalizerAdded"
	ReasonInvalidClass                ConditionReason = "InvalidClass"
	ReasonNetworkRefMissing           ConditionReason = "NetworkRefMissing"
	ReasonOSImageInvalid              ConditionReason = "OSImageInvalid"
	ReasonOSImageNotFound             ConditionReason = "OSImageNotFound"
	ReasonOSImageNotReady             ConditionReason = "OSImageNotReady"
	ReasonImmutableFieldChanged       ConditionReason = "ImmutableFieldChanged"
	ReasonPreflightPassed             ConditionReason = "PreflightPassed"
	ReasonCredentialsResolveFailed    ConditionReason = "CredentialsResolveFailed"
	ReasonCredentialsCreated          ConditionReason = "CredentialsCreated"
	ReasonCredentialsProvisioned      ConditionReason = "CredentialsProvisioned"
	ReasonConnectionSecretReconciled  ConditionReason = "ConnectionSecretReconciled"
	ReasonVMPresent                   ConditionReason = "VMPresent"
	ReasonVMCreateFailed              ConditionReason = "VMCreateFailed"
	ReasonVMCreated                   ConditionReason = "VMCreated"
	ReasonUnsupportedShrink           ConditionReason = "UnsupportedShrink"
	ReasonShapeConverged              ConditionReason = "ShapeConverged"
	ReasonResizeStopping              ConditionReason = "ResizeStopping"
	ReasonResizeWaitingForTeardown    ConditionReason = "ResizeWaitingForTeardown"
	ReasonResizeApplied               ConditionReason = "ResizeApplied"
	ReasonCrashLoopHalted             ConditionReason = "CrashLoopHalted"
	ReasonStartWaitingForTeardown     ConditionReason = "StartWaitingForTeardown"
	ReasonStarting                    ConditionReason = "Starting"
	ReasonRunning                     ConditionReason = "Running"
	ReasonStopping                    ConditionReason = "Stopping"
	ReasonStopped                     ConditionReason = "Stopped"
	ReasonVMBooting                   ConditionReason = "VMBooting"
	ReasonPostgresInitializing        ConditionReason = "PostgresInitializing"
	ReasonPostgresReady               ConditionReason = "PostgresReady"
	ReasonPostgresUnreachable         ConditionReason = "PostgresUnreachable"
	ReasonGuestAgentDisconnected      ConditionReason = "GuestAgentDisconnected"
	ReasonVMRestarting                ConditionReason = "VMRestarting"
	ReasonCrashLoopDetected           ConditionReason = "CrashLoopDetected"
	ReasonRecovered                   ConditionReason = "Recovered"
	ReasonInstanceStopped             ConditionReason = "InstanceStopped"
	ReasonWaitingForEndpoint          ConditionReason = "WaitingForEndpoint"
	ReasonMonitoringDeployFailed      ConditionReason = "MonitoringDeployFailed"
	ReasonMonitoringDeployed          ConditionReason = "MonitoringDeployed"
	ReasonBootstrapCleanupReconciled  ConditionReason = "BootstrapCleanupReconciled"
	ReasonProvisioning                ConditionReason = "Provisioning"
	ReasonDBInstanceReady             ConditionReason = "DBInstanceReady"
	ReasonSpecAccepted                ConditionReason = "SpecAccepted"
	ReasonValidationPending           ConditionReason = "ValidationPending"
	ReasonUnknownValidationFailure    ConditionReason = "UnknownValidationFailure"
	ReasonInterventionRequired        ConditionReason = "InterventionRequired"
	ReasonNoInterventionRequired      ConditionReason = "NoInterventionRequired"
	ReasonDeleting                    ConditionReason = "Deleting"
	ReasonDeletionProtected           ConditionReason = "DeletionProtected"
	ReasonTeardownFailed              ConditionReason = "TeardownFailed"
	ReasonOperatorSecretCleanupFailed ConditionReason = "OperatorSecretCleanupFailed"
	ReasonDeletionProgressing         ConditionReason = "DeletionProgressing"
)

var knownConditionReasons = map[string]ConditionReason{
	string(ReasonFinalizerAdded):              ReasonFinalizerAdded,
	string(ReasonInvalidClass):                ReasonInvalidClass,
	string(ReasonNetworkRefMissing):           ReasonNetworkRefMissing,
	string(ReasonOSImageInvalid):              ReasonOSImageInvalid,
	string(ReasonOSImageNotFound):             ReasonOSImageNotFound,
	string(ReasonOSImageNotReady):             ReasonOSImageNotReady,
	string(ReasonImmutableFieldChanged):       ReasonImmutableFieldChanged,
	string(ReasonPreflightPassed):             ReasonPreflightPassed,
	string(ReasonCredentialsResolveFailed):    ReasonCredentialsResolveFailed,
	string(ReasonCredentialsCreated):          ReasonCredentialsCreated,
	string(ReasonCredentialsProvisioned):      ReasonCredentialsProvisioned,
	string(ReasonConnectionSecretReconciled):  ReasonConnectionSecretReconciled,
	string(ReasonVMPresent):                   ReasonVMPresent,
	string(ReasonVMCreateFailed):              ReasonVMCreateFailed,
	string(ReasonVMCreated):                   ReasonVMCreated,
	string(ReasonUnsupportedShrink):           ReasonUnsupportedShrink,
	string(ReasonShapeConverged):              ReasonShapeConverged,
	string(ReasonResizeStopping):              ReasonResizeStopping,
	string(ReasonResizeWaitingForTeardown):    ReasonResizeWaitingForTeardown,
	string(ReasonResizeApplied):               ReasonResizeApplied,
	string(ReasonCrashLoopHalted):             ReasonCrashLoopHalted,
	string(ReasonStartWaitingForTeardown):     ReasonStartWaitingForTeardown,
	string(ReasonStarting):                    ReasonStarting,
	string(ReasonRunning):                     ReasonRunning,
	string(ReasonStopping):                    ReasonStopping,
	string(ReasonStopped):                     ReasonStopped,
	string(ReasonVMBooting):                   ReasonVMBooting,
	string(ReasonPostgresInitializing):        ReasonPostgresInitializing,
	string(ReasonPostgresReady):               ReasonPostgresReady,
	string(ReasonPostgresUnreachable):         ReasonPostgresUnreachable,
	string(ReasonGuestAgentDisconnected):      ReasonGuestAgentDisconnected,
	string(ReasonVMRestarting):                ReasonVMRestarting,
	string(ReasonCrashLoopDetected):           ReasonCrashLoopDetected,
	string(ReasonRecovered):                   ReasonRecovered,
	string(ReasonInstanceStopped):             ReasonInstanceStopped,
	string(ReasonWaitingForEndpoint):          ReasonWaitingForEndpoint,
	string(ReasonMonitoringDeployFailed):      ReasonMonitoringDeployFailed,
	string(ReasonMonitoringDeployed):          ReasonMonitoringDeployed,
	string(ReasonBootstrapCleanupReconciled):  ReasonBootstrapCleanupReconciled,
	string(ReasonProvisioning):                ReasonProvisioning,
	string(ReasonDBInstanceReady):             ReasonDBInstanceReady,
	string(ReasonSpecAccepted):                ReasonSpecAccepted,
	string(ReasonValidationPending):           ReasonValidationPending,
	string(ReasonUnknownValidationFailure):    ReasonUnknownValidationFailure,
	string(ReasonInterventionRequired):        ReasonInterventionRequired,
	string(ReasonNoInterventionRequired):      ReasonNoInterventionRequired,
	string(ReasonDeleting):                    ReasonDeleting,
	string(ReasonDeletionProtected):           ReasonDeletionProtected,
	string(ReasonTeardownFailed):              ReasonTeardownFailed,
	string(ReasonOperatorSecretCleanupFailed): ReasonOperatorSecretCleanupFailed,
	string(ReasonDeletionProgressing):         ReasonDeletionProgressing,
}

// ParseConditionReason validates a reason already serialized in status.
func ParseConditionReason(value string) (ConditionReason, bool) {
	reason, ok := knownConditionReasons[value]
	return reason, ok
}

// SetCondition adds or updates a status condition. meta.SetStatusCondition only
// bumps LastTransitionTime when Status actually changes, which keeps the
// DeepEqual status-write-skip honest (no spurious writes on unchanged status).
func (s *DBInstanceStatus) SetCondition(c metav1.Condition) {
	meta.SetStatusCondition(&s.Conditions, c)
}

// GetConditions exposes DBInstance conditions through the shared condition
// patching contract.
func (in *DBInstance) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

// SetConditions replaces DBInstance conditions through the shared condition
// patching contract.
func (in *DBInstance) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}

// SetCurrentCondition adds or updates a condition for the DBInstance's current
// generation.
func (in *DBInstance) SetCurrentCondition(conditionType string, status metav1.ConditionStatus, reason ConditionReason, message string) {
	in.Status.SetCondition(metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: in.Generation,
	})
}

// RemoveCondition removes a condition by type.
func (s *DBInstanceStatus) RemoveCondition(conditionType string) {
	meta.RemoveStatusCondition(&s.Conditions, conditionType)
}

// GetCondition returns the condition of the given type, or nil if absent.
func (s *DBInstanceStatus) GetCondition(condType string) *metav1.Condition {
	return meta.FindStatusCondition(s.Conditions, condType)
}

// IsConditionTrue reports whether the named condition is present and True.
func (s *DBInstanceStatus) IsConditionTrue(condType string) bool {
	return meta.IsStatusConditionTrue(s.Conditions, condType)
}

// GetCurrentCondition returns a spec-relative condition only when it describes
// the requested generation.
func (s *DBInstanceStatus) GetCurrentCondition(condType string, generation int64) *metav1.Condition {
	c := s.GetCondition(condType)
	if c == nil || c.ObservedGeneration != generation {
		return nil
	}
	return c
}

func (s *DBInstanceStatus) IsCurrentConditionTrue(condType string, generation int64) bool {
	c := s.GetCurrentCondition(condType, generation)
	return c != nil && c.Status == metav1.ConditionTrue
}

func (s *DBInstanceStatus) IsCurrentConditionFalse(condType string, generation int64) bool {
	c := s.GetCurrentCondition(condType, generation)
	return c != nil && c.Status == metav1.ConditionFalse
}

type PhaseSummary struct {
	Phase   string
	Message string
}

// WantRunning reports the desired power state. An omitted running field
// defaults to true.
func (s *DBInstanceSpec) WantRunning() bool {
	return s.Running == nil || *s.Running
}

func conditionMessage(s *DBInstanceStatus, condType, fallback string) string {
	if c := s.GetCondition(condType); c != nil && c.Message != "" {
		return c.Message
	}
	return fallback
}

func currentConditionHasReason(s *DBInstanceStatus, condType string, generation int64, reasons ...ConditionReason) bool {
	c := s.GetCurrentCondition(condType, generation)
	if c == nil {
		return false
	}
	for _, reason := range reasons {
		if c.Reason == string(reason) {
			return true
		}
	}
	return false
}

// DerivePhaseSummary computes the descriptive phase and matching top-level
// message. Neither value may gate reconciliation.
func DerivePhaseSummary(inst *DBInstance) PhaseSummary {
	s := &inst.Status
	switch {
	case !inst.DeletionTimestamp.IsZero():
		return PhaseSummary{StatusDeleting, conditionMessage(s, ConditionDeletionBlocked, "Tearing down resources")}
	case s.IsConditionTrue(ConditionCrashLoopHalted):
		return PhaseSummary{StatusCrashLoopHalted, conditionMessage(s, ConditionCrashLoopHalted, "Crash-loop halted; operator intervention required")}
	case s.IsCurrentConditionFalse(ConditionAccepted, inst.Generation):
		return PhaseSummary{StatusIncompatibleParameters, conditionMessage(s, ConditionAccepted, "Current specification is not supported")}
	case !inst.Spec.WantRunning() && s.IsCurrentConditionFalse(ConditionPowerStateReady, inst.Generation):
		return PhaseSummary{StatusStopping, conditionMessage(s, ConditionPowerStateReady, "Stopping database instance")}
	case !inst.Spec.WantRunning() && s.IsCurrentConditionTrue(ConditionPowerStateReady, inst.Generation):
		return PhaseSummary{StatusStopped, conditionMessage(s, ConditionPowerStateReady, "Stopped. Storage preserved.")}
	case s.IsConditionTrue(ConditionDegraded):
		return PhaseSummary{StatusDegraded, conditionMessage(s, ConditionDegraded, "Database instance is degraded")}
	case s.IsConditionTrue(ConditionResizeInProgress):
		return PhaseSummary{StatusModifying, conditionMessage(s, ConditionResizeInProgress, "Applying database instance resize")}
	case s.ObservedGeneration > 0 && inst.Spec.WantRunning() &&
		s.IsConditionTrue(ConditionDatabaseReady) &&
		s.IsCurrentConditionFalse(ConditionMonitoringReady, inst.Generation) &&
		currentConditionHasReason(s, ConditionMonitoringReady, inst.Generation, ReasonMonitoringDeployFailed):
		return PhaseSummary{StatusDegraded, conditionMessage(s, ConditionMonitoringReady, "Database is available, but monitoring is not ready")}
	case s.ObservedGeneration > 0 && inst.Spec.WantRunning() &&
		s.IsCurrentConditionFalse(ConditionPowerStateReady, inst.Generation) &&
		currentConditionHasReason(s, ConditionPowerStateReady, inst.Generation, ReasonStarting, ReasonStartWaitingForTeardown):
		return PhaseSummary{StatusStarting, conditionMessage(s, ConditionPowerStateReady, "Starting database instance")}
	case s.ObservedGeneration > 0 && inst.Spec.WantRunning() &&
		s.IsCurrentConditionFalse(ConditionReady, inst.Generation):
		// Power convergence is only the first half of starting a stopped
		// instance. Keep the lifecycle phase until PostgreSQL is ready too.
		return PhaseSummary{StatusStarting, conditionMessage(s, ConditionReady, "Starting database instance")}
	case s.IsConditionTrue(ConditionReady):
		return PhaseSummary{StatusAvailable, "Database instance is available"}
	default:
		return PhaseSummary{StatusCreating, "Creating database instance"}
	}
}
