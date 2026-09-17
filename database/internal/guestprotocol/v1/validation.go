package guestprotocolv1

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ProtocolMajor     = 1
	TaskSchemaVersion = 1
	MaxEncodedSize    = 64 * 1024
	// Local envelopes carry a full-sized command/event plus correlation metadata.
	MaxLocalEncodedSize = 128 * 1024
	CredentialAlgorithm = "age-x25519-v1"
)

// Validation errors contain field names, never untrusted values or SDK errors.
type ValidationError struct {
	Code  ErrorCode
	Field string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Field) }
func invalid(field string) error {
	return &ValidationError{Code: ErrorCode_ERROR_CODE_INVALID_COMMAND, Field: field}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var labelPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}F(_[0-9]{8}-[0-9]{6}[DI])?$`)
var systemIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var windowPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]-([01][0-9]|2[0-3]):[0-5][0-9]$`)

func validID(id string) bool                  { return idPattern.MatchString(id) }
func timestamp(t *timestamppb.Timestamp) bool { return t != nil && t.CheckValid() == nil }
func validState(s OperationState) bool {
	return s >= OperationState_OPERATION_STATE_ADMITTED && s <= OperationState_OPERATION_STATE_OUTCOME_UNKNOWN
}
func validSnapshot(s SnapshotType) bool {
	return s == SnapshotType_SNAPSHOT_TYPE_MANUAL || s == SnapshotType_SNAPSHOT_TYPE_FINAL
}

// unknownFields rejects semantics this implementation cannot understand before
// hashing or admission. Adding supported fields requires an explicit rollout.
func unknownFields(m protoreflect.Message) bool {
	if !m.IsValid() || len(m.GetUnknown()) != 0 {
		return true
	}
	bad := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		check := func(v protoreflect.Value) bool {
			if fd.Kind() == protoreflect.MessageKind {
				return unknownFields(v.Message())
			}
			return fd.Kind() == protoreflect.EnumKind && fd.Enum().Values().ByNumber(v.Enum()) == nil
		}
		if fd.IsList() {
			for i := 0; i < v.List().Len(); i++ {
				if check(v.List().Get(i)) {
					bad = true
					break
				}
			}
		} else {
			bad = check(v)
		}
		return !bad
	})
	return bad
}

func commandShape(c *Command) error {
	if c == nil {
		return invalid("command")
	}
	if proto.Size(c) > MaxEncodedSize {
		return invalid("encoded_size")
	}
	if unknownFields(c.ProtoReflect()) {
		return invalid("unknown_fields")
	}
	if c.ProtocolMajor != ProtocolMajor {
		return &ValidationError{Code: ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH, Field: "protocol_major"}
	}
	if !validID(c.MessageId) || !validID(c.OperationId) || !validID(c.InstanceUid) || !validID(c.ResourceUid) {
		return invalid("identity")
	}
	if !timestamp(c.IssuedAt) || !timestamp(c.AdmissionExpiresAt) || !timestamp(c.OperationDeadline) {
		return invalid("timestamps")
	}
	if !c.IssuedAt.AsTime().Before(c.AdmissionExpiresAt.AsTime()) || c.AdmissionExpiresAt.AsTime().After(c.OperationDeadline.AsTime()) {
		return invalid("time_order")
	}
	if err := commandBody(c); err != nil {
		return err
	}
	return nil
}

func repository(r *RepositoryIdentity) error {
	if r == nil || !validID(r.RepositoryUid) || !validID(r.SourceInstanceUid) {
		return invalid("repository.identity")
	}
	if _, err := normalizedEndpoint(r.Endpoint); err != nil {
		return err
	}
	if !bucketPattern.MatchString(r.Bucket) || strings.Contains(r.Bucket, "..") {
		return invalid("repository.bucket")
	}
	if _, err := normalizedPrefix(r.Prefix); err != nil {
		return err
	}
	if r.Region != "" && !validID(r.Region) {
		return invalid("repository.region")
	}
	return nil
}

func normalizedEndpoint(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || len(s) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") {
		return "", invalid("repository.endpoint")
	}
	// Credentials, query parameters and arbitrary endpoint paths are forbidden.
	host := strings.ToLower(u.Host)
	if strings.HasSuffix(host, ":443") {
		host = strings.TrimSuffix(host, ":443")
	}
	return "https://" + host, nil
}

func normalizedPrefix(s string) (string, error) {
	if len(s) > 1024 {
		return "", invalid("repository.prefix")
	}
	s = strings.Trim(s, "/")
	if s == "" {
		return "", invalid("repository.prefix")
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return "", invalid("repository.prefix")
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return "", invalid("repository.prefix")
			}
		}
	}
	return s, nil
}

func credential(c *EncryptedCredential) error {
	if c == nil || !validID(c.CredentialUid) || !validID(c.Version) || !validID(c.KeyId) || c.Algorithm != CredentialAlgorithm || len(c.Ciphertext) == 0 {
		return invalid("credential")
	}
	return nil
}

func source(s *RestoreSource) error {
	if s == nil {
		return invalid("restore_source")
	}
	if err := repository(s.Repository); err != nil {
		return err
	}
	if !labelPattern.MatchString(s.BackupLabel) || s.PostgresMajor == 0 || !systemIDPattern.MatchString(s.SystemIdentifier) || s.Timeline == 0 {
		return invalid("restore_source.lineage")
	}
	switch target := s.RecoveryTarget.(type) {
	case *RestoreSource_TargetTime:
		if target == nil || !timestamp(target.TargetTime) {
			return invalid("restore_source.target_time")
		}
	case *RestoreSource_Snapshot:
		if target == nil || target.Snapshot == nil || !validID(target.Snapshot.SnapshotUid) || !validSnapshot(target.Snapshot.Type) {
			return invalid("restore_source.snapshot")
		}
	default:
		return invalid("restore_source.recovery_target")
	}
	return nil
}

func hold(h *RestoreHold, c *Command) error {
	if h == nil || !validID(h.HoldId) || h.Generation == 0 || !validID(h.TargetInstanceUid) {
		return invalid("hold")
	}
	if err := source(h.Source); err != nil {
		return err
	}
	if h.Source.GetTargetTime() == nil || h.Source.Repository.SourceInstanceUid != c.InstanceUid || h.TargetInstanceUid == c.InstanceUid {
		return invalid("hold.source_binding")
	}
	return nil
}

func sections(s *InspectState) error {
	if s == nil || len(s.Sections) == 0 || len(s.Sections) > 4 {
		return invalid("inspect.sections")
	}
	seen := map[StateSection]bool{}
	for _, section := range s.Sections {
		if section < StateSection_STATE_SECTION_POLICY || section > StateSection_STATE_SECTION_SAFETY || seen[section] {
			return invalid("inspect.sections")
		}
		seen[section] = true
	}
	return nil
}

func commandBody(c *Command) error {
	var repo *RepositoryIdentity
	var cred *EncryptedCredential
	switch p := c.Body.(type) {
	case *Command_ValidateRepository:
		if p == nil || p.ValidateRepository == nil {
			return invalid("validate_repository")
		}
		repo = p.ValidateRepository.Repository
		cred = p.ValidateRepository.Credential
	case *Command_ApplyBackupPolicy:
		if p == nil || p.ApplyBackupPolicy == nil {
			return invalid("apply_backup_policy")
		}
		b := p.ApplyBackupPolicy
		if b.PolicyGeneration == 0 || b.RetentionDays < 1 || b.RetentionDays > 35 || !windowPattern.MatchString(b.PreferredWindowUtc) || b.PreferredWindowUtc[:5] == b.PreferredWindowUtc[6:] {
			return invalid("backup_policy")
		}
		repo = b.Repository
		cred = b.Credential
	case *Command_RunBackup:
		if p == nil || p.RunBackup == nil {
			return invalid("run_backup")
		}
		b := p.RunBackup
		if b.PolicyGeneration == 0 || (b.Type != BackupType_BACKUP_TYPE_FULL && b.Type != BackupType_BACKUP_TYPE_DIFFERENTIAL) || b.Reason < BackupReason_BACKUP_REASON_INITIAL || b.Reason > BackupReason_BACKUP_REASON_CATCH_UP {
			return invalid("run_backup")
		}
		if b.Reason == BackupReason_BACKUP_REASON_INITIAL && b.Type != BackupType_BACKUP_TYPE_FULL {
			return invalid("initial_backup.type")
		}
		repo = b.Repository
	case *Command_CreateSnapshot:
		if p == nil || p.CreateSnapshot == nil {
			return invalid("create_snapshot")
		}
		b := p.CreateSnapshot
		if !validID(b.SnapshotUid) || b.SnapshotUid != c.ResourceUid || !validSnapshot(b.Type) {
			return invalid("snapshot.identity")
		}
		repo = b.Repository
		cred = b.Credential
	case *Command_DisableAutomatedBackup:
		if p == nil || p.DisableAutomatedBackup == nil || p.DisableAutomatedBackup.PolicyGeneration == 0 {
			return invalid("disable_automated_backup")
		}
		return nil
	case *Command_DeleteAutomatedRecoveryData:
		if p == nil || p.DeleteAutomatedRecoveryData == nil {
			return invalid("delete_automated_recovery_data")
		}
		b := p.DeleteAutomatedRecoveryData
		if b.DeletingResourceUid != c.ResourceUid || b.PolicyGeneration == 0 || b.DeletionGeneration == 0 {
			return invalid("deletion.identity")
		}
		repo = b.Repository
	case *Command_AcquireRestoreHold:
		if p == nil || p.AcquireRestoreHold == nil {
			return invalid("acquire_restore_hold")
		}
		return hold(p.AcquireRestoreHold.Hold, c)
	case *Command_ReleaseRestoreHold:
		if p == nil || p.ReleaseRestoreHold == nil {
			return invalid("release_restore_hold")
		}
		b := p.ReleaseRestoreHold
		if (b.Reason != HoldReleaseReason_HOLD_RELEASE_REASON_SOURCE_READS_FINISHED && b.Reason != HoldReleaseReason_HOLD_RELEASE_REASON_TARGET_FENCED) || !validID(b.EvidenceOperationId) {
			return invalid("hold.release_evidence")
		}
		return hold(b.Hold, c)
	case *Command_RestoreDatabase:
		if p == nil || p.RestoreDatabase == nil {
			return invalid("restore_database")
		}
		b := p.RestoreDatabase
		if err := source(b.Source); err != nil {
			return err
		}
		if b.TargetInstanceUid != c.InstanceUid || !validID(b.TargetVolumeUid) || !validID(b.HoldId) || b.HoldGeneration == 0 || b.Source.Repository.SourceInstanceUid == c.InstanceUid {
			return invalid("restore.target_binding")
		}
		return credential(b.Credential)
	case *Command_CancelOperation:
		if p == nil || p.CancelOperation == nil || !validID(p.CancelOperation.TargetOperationId) || p.CancelOperation.TargetOperationId == c.OperationId {
			return invalid("cancel.target")
		}
		return nil
	case *Command_InspectState:
		if p == nil {
			return invalid("inspect_state")
		}
		return sections(p.InspectState)
	default:
		return invalid("command.body")
	}
	if err := repository(repo); err != nil {
		return err
	}
	if repo.SourceInstanceUid != c.InstanceUid {
		return invalid("repository.source_binding")
	}
	switch c.Body.(type) {
	case *Command_ValidateRepository, *Command_ApplyBackupPolicy, *Command_CreateSnapshot:
		return credential(cred)
	}
	return nil
}

// ValidateCommand checks immutable structure and intent, not current time or
// authorization. Trusted target checks must precede persistent duplicate lookup.
func ValidateCommand(c *Command) error {
	if err := commandShape(c); err != nil {
		return err
	}
	digest, err := IntentDigest(c)
	if err != nil {
		return err
	}
	if !bytes.Equal(c.IntentDigest, digest) {
		return &ValidationError{Code: ErrorCode_ERROR_CODE_OPERATION_CONFLICT, Field: "intent_digest"}
	}
	return nil
}

func ValidateTarget(c *Command, instanceUID, resourceUID string) error {
	if c == nil || !validID(instanceUID) || !validID(resourceUID) || c.InstanceUid != instanceUID || c.ResourceUid != resourceUID {
		return &ValidationError{Code: ErrorCode_ERROR_CODE_IDENTITY_MISMATCH, Field: "target"}
	}
	return nil
}

// ValidateNewAdmission is called only after an authenticated duplicate lookup
// found no receipt. Existing matching receipts remain valid after expiry.
func ValidateNewAdmission(c *Command, now time.Time) error {
	if err := ValidateCommand(c); err != nil {
		return err
	}
	if !now.Before(c.OperationDeadline.AsTime()) {
		return &ValidationError{Code: ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, Field: "operation_deadline"}
	}
	if now.Before(c.IssuedAt.AsTime()) || !now.Before(c.AdmissionExpiresAt.AsTime()) {
		return &ValidationError{Code: ErrorCode_ERROR_CODE_ADMISSION_EXPIRED, Field: "admission_expires_at"}
	}
	return nil
}

// MatchReceipt checks both stable identities; the caller must enforce unique
// message AND operation IDs transactionally in SQLite (step 04/06).
func MatchReceipt(c *Command, r *AdmissionReceipt) error {
	if err := ValidateCommand(c); err != nil {
		return err
	}
	if err := validateReceipt(r); err != nil {
		return err
	}
	if c.MessageId != r.MessageId || c.OperationId != r.OperationId || c.InstanceUid != r.InstanceUid || c.ResourceUid != r.ResourceUid || !bytes.Equal(c.IntentDigest, r.IntentDigest) {
		return &ValidationError{Code: ErrorCode_ERROR_CODE_OPERATION_CONFLICT, Field: "receipt"}
	}
	return nil
}

func validateReceipt(r *AdmissionReceipt) error {
	if r == nil || !validID(r.MessageId) || !validID(r.OperationId) || !validID(r.InstanceUid) || !validID(r.ResourceUid) || len(r.IntentDigest) != 32 || !timestamp(r.AdmittedAt) || !validState(r.State) {
		return invalid("receipt")
	}
	return nil
}
