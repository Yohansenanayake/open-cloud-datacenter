package guestprotocolv1

import (
	"fmt"
	"google.golang.org/protobuf/proto"
)

// NewSafeError deliberately takes no free-form message or underlying error.
// Detailed SDK/subprocess errors must not cross the guest protocol boundary.
func NewSafeError(code ErrorCode) *SafeError {
	e := &SafeError{Code: code}
	switch code {
	case ErrorCode_ERROR_CODE_INVALID_COMMAND:
		e.Message = "Invalid command"
	case ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH:
		e.Message = "Unsupported protocol version"
	case ErrorCode_ERROR_CODE_IDENTITY_MISMATCH:
		e.Message = "Target identity does not match"
	case ErrorCode_ERROR_CODE_OPERATION_CONFLICT:
		e.Message = "Operation identity conflicts with accepted intent"
	case ErrorCode_ERROR_CODE_ADMISSION_EXPIRED:
		e.Message = "Command admission window has expired"
	case ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
		e.Message = "Operation deadline has elapsed"
	case ErrorCode_ERROR_CODE_BACKLOG_FULL:
		e.Message = "Admission capacity is exhausted"
		e.Retryable = true
	case ErrorCode_ERROR_CODE_STALE_GENERATION:
		e.Message = "Configuration generation is stale"
	case ErrorCode_ERROR_CODE_PREREQUISITE_PENDING:
		e.Message = "Required prerequisite is pending"
		e.Retryable = true
	case ErrorCode_ERROR_CODE_STORAGE_UNAVAILABLE:
		e.Message = "Required storage is unavailable"
		e.Retryable = true
	case ErrorCode_ERROR_CODE_OUTCOME_UNKNOWN:
		e.Message = "Physical outcome requires reconciliation"
	default:
		e.Code = ErrorCode_ERROR_CODE_INTERNAL
		e.Message = "Internal operation failure"
	}
	return e
}
func safeError(e *SafeError) error {
	if e == nil || e.Code <= ErrorCode_ERROR_CODE_UNSPECIFIED || e.Code > ErrorCode_ERROR_CODE_INTERNAL {
		return invalid("error.code")
	}
	want := NewSafeError(e.Code)
	if e.Message != want.Message || e.Retryable != want.Retryable {
		return invalid("error.sanitization")
	}
	return nil
}

// CommandSummary is the logging boundary; never log generated message String()
// methods, raw wire bytes, repository configuration or credential bundles.
func CommandSummary(c *Command) string {
	if c == nil {
		return "command=<nil>"
	}
	id := func(s string) string {
		if !validID(s) {
			return "<invalid>"
		}
		return s
	}
	return fmt.Sprintf("command=%s message_id=%s operation_id=%s instance_uid=%s resource_uid=%s", Kind(c), id(c.MessageId), id(c.OperationId), id(c.InstanceUid), id(c.ResourceUid))
}
func Kind(c *Command) CommandKind {
	if c == nil {
		return CommandKind_COMMAND_KIND_UNSPECIFIED
	}
	switch c.Body.(type) {
	case *Command_ValidateRepository:
		return CommandKind_COMMAND_KIND_VALIDATE_REPOSITORY
	case *Command_ApplyBackupPolicy:
		return CommandKind_COMMAND_KIND_APPLY_BACKUP_POLICY
	case *Command_RunBackup:
		return CommandKind_COMMAND_KIND_RUN_BACKUP
	case *Command_CreateSnapshot:
		return CommandKind_COMMAND_KIND_CREATE_SNAPSHOT
	case *Command_DisableAutomatedBackup:
		return CommandKind_COMMAND_KIND_DISABLE_AUTOMATED_BACKUP
	case *Command_DeleteAutomatedRecoveryData:
		return CommandKind_COMMAND_KIND_DELETE_AUTOMATED_RECOVERY_DATA
	case *Command_AcquireRestoreHold:
		return CommandKind_COMMAND_KIND_ACQUIRE_RESTORE_HOLD
	case *Command_ReleaseRestoreHold:
		return CommandKind_COMMAND_KIND_RELEASE_RESTORE_HOLD
	case *Command_RestoreDatabase:
		return CommandKind_COMMAND_KIND_RESTORE_DATABASE
	case *Command_CancelOperation:
		return CommandKind_COMMAND_KIND_CANCEL_OPERATION
	case *Command_InspectState:
		return CommandKind_COMMAND_KIND_INSPECT_STATE
	default:
		return CommandKind_COMMAND_KIND_UNSPECIFIED
	}
}

func capabilities(h *AgentHello) error {
	if h == nil || len(h.ProtocolMajors) == 0 || h.TaskSchemaVersion != TaskSchemaVersion || !validID(h.ExecutorVersion) {
		return invalid("capabilities")
	}
	versions := map[uint32]bool{}
	supported := false
	for _, v := range h.ProtocolMajors {
		if v == 0 || versions[v] {
			return invalid("capabilities.protocols")
		}
		versions[v] = true
		supported = supported || v == ProtocolMajor
	}
	if !supported {
		return invalid("capabilities.protocols")
	}
	kinds := map[CommandKind]bool{}
	for _, k := range h.Capabilities {
		if k < CommandKind_COMMAND_KIND_VALIDATE_REPOSITORY || k > CommandKind_COMMAND_KIND_INSPECT_STATE || kinds[k] {
			return invalid("capabilities.commands")
		}
		kinds[k] = true
	}
	return nil
}

func artifact(a *BackupArtifact) error {
	if a == nil || !labelPattern.MatchString(a.BackupLabel) || !timestamp(a.CompletedAt) || a.PostgresMajor == 0 || !systemIDPattern.MatchString(a.SystemIdentifier) || a.Timeline == 0 {
		return invalid("backup_artifact")
	}
	return repository(a.Repository)
}
func health(h *BackupHealth) error {
	if h == nil || !timestamp(h.ObservedAt) || h.Scheduler < HealthState_HEALTH_STATE_HEALTHY || h.Scheduler > HealthState_HEALTH_STATE_DISABLED || h.Repository < HealthState_HEALTH_STATE_HEALTHY || h.Repository > HealthState_HEALTH_STATE_DISABLED || h.Archiver < HealthState_HEALTH_STATE_HEALTHY || h.Archiver > HealthState_HEALTH_STATE_DISABLED {
		return invalid("backup_health")
	}
	if h.AutomationEnabled && h.AppliedPolicyGeneration == 0 {
		return invalid("backup_health.generation")
	}
	if h.Pitr < HealthState_HEALTH_STATE_HEALTHY || h.Pitr > HealthState_HEALTH_STATE_DISABLED {
		return invalid("backup_health.pitr")
	}
	if h.AppliedCredentialVersion != "" && !validID(h.AppliedCredentialVersion) {
		return invalid("backup_health.credential_version")
	}
	if h.RepositoryIdentity != nil {
		if err := repository(h.RepositoryIdentity); err != nil {
			return err
		}
	}
	if h.AutomationEnabled && (h.RepositoryIdentity == nil || h.AppliedCredentialVersion == "") {
		return invalid("backup_health.policy_binding")
	}
	if h.Pitr == HealthState_HEALTH_STATE_HEALTHY && (h.RecoveryBounds == nil || !h.InitialBackupComplete || !h.AutomationEnabled) {
		return invalid("backup_health.pitr_evidence")
	}
	if h.LastBackup != nil {
		if err := artifact(h.LastBackup); err != nil {
			return err
		}
	}
	if h.LastArchiveUploadAt != nil && !timestamp(h.LastArchiveUploadAt) {
		return invalid("backup_health.archive_time")
	}
	if r := h.RecoveryBounds; r != nil {
		if !timestamp(r.EarliestRestorableTime) || !timestamp(r.LatestRestorableTime) || !timestamp(r.VerifiedAt) || r.EarliestRestorableTime.AsTime().After(r.LatestRestorableTime.AsTime()) || r.LatestRestorableTime.AsTime().After(r.VerifiedAt.AsTime()) || r.VerifiedAt.AsTime().After(h.ObservedAt.AsTime()) || !systemIDPattern.MatchString(r.SystemIdentifier) || r.Timeline == 0 || !labelPattern.MatchString(r.BaseBackupLabel) {
			return invalid("backup_health.recovery_bounds")
		}
	}
	if h.Error != nil {
		return safeError(h.Error)
	}
	return nil
}
func inspection(s *StateInspection) error {
	if s == nil {
		return invalid("inspection")
	}
	if s.BackupHealth != nil {
		if err := health(s.BackupHealth); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, op := range s.Operations {
		if op == nil || !validID(op.OperationId) || !validState(op.State) || op.LastEventSequence == 0 || seen[op.OperationId] {
			return invalid("inspection.operations")
		}
		seen[op.OperationId] = true
	}
	if safety := s.Safety; safety != nil {
		if safety.CleanupPending && safety.CleanupGeneration == 0 {
			return invalid("inspection.cleanup_generation")
		}
		holds := map[string]bool{}
		for _, h := range safety.Holds {
			if h == nil || !validID(h.HoldId) || h.Generation == 0 || !validID(h.TargetInstanceUid) || holds[h.HoldId] {
				return invalid("inspection.holds")
			}
			holds[h.HoldId] = true
			if err := source(h.Source); err != nil {
				return err
			}
		}
	}
	return nil
}

func operationStatus(s *OperationStatus) error {
	if s == nil || !validState(s.State) {
		return invalid("operation_status.state")
	}
	if s.Error != nil {
		if err := safeError(s.Error); err != nil {
			return err
		}
	}
	if s.State == OperationState_OPERATION_STATE_SUCCEEDED && (s.Result == nil || s.Error != nil) {
		return invalid("operation_status.success")
	}
	if (s.State == OperationState_OPERATION_STATE_FAILED || s.State == OperationState_OPERATION_STATE_OUTCOME_UNKNOWN || s.State == OperationState_OPERATION_STATE_RETRY_WAIT) && s.Error == nil {
		return invalid("operation_status.error")
	}
	switch r := s.Result.(type) {
	case nil:
		return nil
	case *OperationStatus_RepositoryValidated:
		if r == nil || r.RepositoryValidated == nil || !validID(r.RepositoryValidated.CredentialVersion) {
			return invalid("repository_validated")
		}
		return repository(r.RepositoryValidated.Repository)
	case *OperationStatus_PolicyApplied:
		if r == nil || r.PolicyApplied == nil || r.PolicyApplied.PolicyGeneration == 0 || !validID(r.PolicyApplied.CredentialVersion) {
			return invalid("policy_applied")
		}
	case *OperationStatus_Backup:
		if r == nil {
			return invalid("backup")
		}
		return artifact(r.Backup)
	case *OperationStatus_SnapshotCreated:
		if r == nil || r.SnapshotCreated == nil || !validID(r.SnapshotCreated.SnapshotUid) || !validSnapshot(r.SnapshotCreated.Type) {
			return invalid("snapshot_created")
		}
		return artifact(r.SnapshotCreated.Artifact)
	case *OperationStatus_AutomationDisabled:
		if r == nil || r.AutomationDisabled == nil || r.AutomationDisabled.PolicyGeneration == 0 {
			return invalid("automation_disabled")
		}
	case *OperationStatus_AutomatedDataDeleted:
		if r == nil || r.AutomatedDataDeleted == nil || !validID(r.AutomatedDataDeleted.RepositoryUid) || r.AutomatedDataDeleted.DeletionGeneration == 0 {
			return invalid("automated_data_deleted")
		}
	case *OperationStatus_HoldAcknowledged:
		if r == nil || r.HoldAcknowledged == nil || !validID(r.HoldAcknowledged.HoldId) || r.HoldAcknowledged.Generation == 0 {
			return invalid("hold_acknowledged")
		}
	case *OperationStatus_RestoreCompleted:
		if r == nil || r.RestoreCompleted == nil {
			return invalid("restore_completed")
		}
		b := r.RestoreCompleted
		if !validID(b.TargetVolumeUid) || !b.RecoveryEnded || !b.ManagedCredentialsReplaced || !b.Writable || !b.SourceReadsFinished {
			return invalid("restore_completed.verification")
		}
		return source(b.Source)
	case *OperationStatus_CancellationRecorded:
		if r == nil || r.CancellationRecorded == nil || !validID(r.CancellationRecorded.TargetOperationId) {
			return invalid("cancellation_recorded")
		}
	case *OperationStatus_Inspection:
		if r == nil {
			return invalid("inspection")
		}
		return inspection(r.Inspection)
	default:
		return invalid("operation_status.result")
	}
	return nil
}

func ValidateEvent(e *Event) error {
	if e != nil && proto.Size(e) > MaxEncodedSize {
		return invalid("encoded_size")
	}
	if e == nil || e.ProtocolMajor != ProtocolMajor || !validID(e.EventId) || !validID(e.InstanceUid) || !validID(e.ResourceUid) || !timestamp(e.OccurredAt) || !validID(e.AgentVersion) || unknownFields(e.ProtoReflect()) {
		return invalid("event.envelope")
	}
	caused := false
	switch e.Body.(type) {
	case *Event_Admitted, *Event_Rejected, *Event_OperationStatus:
		caused = true
	}
	if caused {
		if !validID(e.OperationId) || !validID(e.CommandMessageId) {
			return invalid("event.operation_identity")
		}
		_, rejected := e.Body.(*Event_Rejected)
		if (rejected && e.EventSequence != 0) || (!rejected && e.EventSequence == 0) {
			return invalid("event.sequence")
		}
	} else if e.OperationId != "" || e.CommandMessageId != "" || e.EventSequence != 0 {
		return invalid("event.unsolicited_identity")
	}
	switch b := e.Body.(type) {
	case *Event_Hello:
		if b == nil {
			return invalid("hello")
		}
		return capabilities(b.Hello)
	case *Event_Admitted:
		if b == nil {
			return invalid("admitted")
		}
		r := b.Admitted
		if err := validateReceipt(r); err != nil {
			return err
		}
		if r.MessageId != e.CommandMessageId || r.OperationId != e.OperationId || r.InstanceUid != e.InstanceUid || r.ResourceUid != e.ResourceUid || r.State != OperationState_OPERATION_STATE_ADMITTED {
			return invalid("event.receipt_binding")
		}
	case *Event_Rejected:
		if b == nil {
			return invalid("rejected")
		}
		return safeError(b.Rejected)
	case *Event_OperationStatus:
		if b == nil {
			return invalid("operation_status")
		}
		return operationStatus(b.OperationStatus)
	case *Event_BackupHealth:
		if b == nil {
			return invalid("backup_health")
		}
		return health(b.BackupHealth)
	case *Event_Heartbeat:
		if b == nil || b.Heartbeat == nil {
			return invalid("heartbeat")
		}
	default:
		return invalid("event.body")
	}
	return nil
}

func ValidateLocalRequest(r *LocalRequest) error {
	if r == nil || r.ProtocolMajor != ProtocolMajor || !validID(r.RequestId) || !validID(r.InstanceUid) || unknownFields(r.ProtoReflect()) {
		return invalid("local_request.envelope")
	}
	switch b := r.Body.(type) {
	case *LocalRequest_Submit:
		if b == nil || b.Submit == nil || b.Submit.InstanceUid != r.InstanceUid {
			return invalid("local_request.target")
		}
		return ValidateCommand(b.Submit)
	case *LocalRequest_Inspect:
		if b == nil {
			return invalid("inspect")
		}
		return sections(b.Inspect)
	case *LocalRequest_ReadPendingEvents:
		if b == nil || b.ReadPendingEvents == nil || b.ReadPendingEvents.Limit == 0 || b.ReadPendingEvents.Limit > 64 {
			return invalid("pending_events.limit")
		}
	case *LocalRequest_AcknowledgeEvents:
		if b == nil || b.AcknowledgeEvents == nil {
			return invalid("acknowledge_events")
		}
		return eventIDs(b.AcknowledgeEvents.EventIds)
	case *LocalRequest_GetCapabilities:
		if b == nil || b.GetCapabilities == nil {
			return invalid("get_capabilities")
		}
	default:
		return invalid("local_request.body")
	}
	return nil
}
func eventIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > 64 {
		return invalid("event_ids")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return invalid("event_ids")
		}
		seen[id] = true
	}
	return nil
}
func ValidateLocalResponse(r *LocalResponse) error {
	if r == nil || r.ProtocolMajor != ProtocolMajor || !validID(r.RequestId) || unknownFields(r.ProtoReflect()) {
		return invalid("local_response.envelope")
	}
	switch b := r.Body.(type) {
	case *LocalResponse_Submission:
		if b == nil || b.Submission == nil || b.Submission.ProtocolMajor != ProtocolMajor {
			return invalid("submission")
		}
		switch outcome := b.Submission.Outcome.(type) {
		case *SubmitResponse_Receipt:
			if outcome == nil {
				return invalid("receipt")
			}
			return validateReceipt(outcome.Receipt)
		case *SubmitResponse_Rejection:
			if outcome == nil {
				return invalid("rejection")
			}
			return safeError(outcome.Rejection)
		default:
			return invalid("submission.outcome")
		}
	case *LocalResponse_Inspection:
		if b == nil {
			return invalid("inspection")
		}
		return inspection(b.Inspection)
	case *LocalResponse_PendingEvents:
		if b == nil || b.PendingEvents == nil || len(b.PendingEvents.Events) > 64 {
			return invalid("pending_events")
		}
		seen := map[string]bool{}
		for _, e := range b.PendingEvents.Events {
			if err := ValidateEvent(e); err != nil {
				return err
			}
			if seen[e.EventId] {
				return invalid("pending_events.duplicate")
			}
			seen[e.EventId] = true
		}
	case *LocalResponse_EventsAcknowledged:
		if b == nil || b.EventsAcknowledged == nil {
			return invalid("events_acknowledged")
		}
		return eventIDs(b.EventsAcknowledged.EventIds)
	case *LocalResponse_Capabilities:
		if b == nil {
			return invalid("capabilities")
		}
		return capabilities(b.Capabilities)
	case *LocalResponse_Error:
		if b == nil {
			return invalid("error")
		}
		return safeError(b.Error)
	default:
		return invalid("local_response.body")
	}
	return nil
}
