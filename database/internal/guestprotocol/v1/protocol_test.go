package guestprotocolv1

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var testTime = time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)

func ts(offset time.Duration) *timestamppb.Timestamp { return timestamppb.New(testTime.Add(offset)) }
func testRepo() *RepositoryIdentity {
	return &RepositoryIdentity{RepositoryUid: "repo-1", SourceInstanceUid: "vm-1", Endpoint: "https://s3.example.test", Bucket: "backups", Prefix: "tenant/instance", Region: "us-east-1"}
}
func testCredential() *EncryptedCredential {
	return &EncryptedCredential{CredentialUid: "secret-1", Version: "42", KeyId: "key-1", Algorithm: CredentialAlgorithm, Ciphertext: []byte("opaque encrypted fixture")}
}
func testSource() *RestoreSource {
	return &RestoreSource{Repository: testRepo(), BackupLabel: "20260909-090000F", PostgresMajor: 17, SystemIdentifier: "123456789", Timeline: 1, RecoveryTarget: &RestoreSource_TargetTime{TargetTime: ts(-time.Minute)}}
}
func testHold() *RestoreHold {
	return &RestoreHold{HoldId: "hold-1", Generation: 1, TargetInstanceUid: "vm-2", Source: testSource()}
}
func commandFixtures() map[string]*Command {
	bodies := map[string]isCommand_Body{
		"validate": &Command_ValidateRepository{ValidateRepository: &ValidateRepository{Repository: testRepo(), Credential: testCredential()}},
		"policy":   &Command_ApplyBackupPolicy{ApplyBackupPolicy: &ApplyBackupPolicy{PolicyGeneration: 1, Repository: testRepo(), Credential: testCredential(), RetentionDays: 7, PreferredWindowUtc: "23:30-01:30"}},
		"backup":   &Command_RunBackup{RunBackup: &RunBackup{Repository: testRepo(), PolicyGeneration: 1, Type: BackupType_BACKUP_TYPE_FULL, Reason: BackupReason_BACKUP_REASON_INITIAL}},
		"snapshot": &Command_CreateSnapshot{CreateSnapshot: &CreateSnapshot{SnapshotUid: "resource-1", Type: SnapshotType_SNAPSHOT_TYPE_MANUAL, Repository: testRepo(), Credential: testCredential()}},
		"disable":  &Command_DisableAutomatedBackup{DisableAutomatedBackup: &DisableAutomatedBackup{PolicyGeneration: 2}},
		"delete":   &Command_DeleteAutomatedRecoveryData{DeleteAutomatedRecoveryData: &DeleteAutomatedRecoveryData{Repository: testRepo(), DeletingResourceUid: "resource-1", PolicyGeneration: 2, DeletionGeneration: 1}},
		"acquire":  &Command_AcquireRestoreHold{AcquireRestoreHold: &AcquireRestoreHold{Hold: testHold()}},
		"release":  &Command_ReleaseRestoreHold{ReleaseRestoreHold: &ReleaseRestoreHold{Hold: testHold(), Reason: HoldReleaseReason_HOLD_RELEASE_REASON_SOURCE_READS_FINISHED, EvidenceOperationId: "restore-op"}},
		"restore":  &Command_RestoreDatabase{RestoreDatabase: &RestoreDatabase{Source: testSource(), TargetInstanceUid: "vm-2", TargetVolumeUid: "volume-2", Credential: testCredential(), HoldId: "hold-1", HoldGeneration: 1}},
		"cancel":   &Command_CancelOperation{CancelOperation: &CancelOperation{TargetOperationId: "other-op"}},
		"inspect":  &Command_InspectState{InspectState: &InspectState{Sections: []StateSection{StateSection_STATE_SECTION_SAFETY, StateSection_STATE_SECTION_POLICY}}},
	}
	commands := map[string]*Command{}
	for name, body := range bodies {
		c := &Command{ProtocolMajor: ProtocolMajor, MessageId: "message-1", OperationId: "operation-1", InstanceUid: "vm-1", ResourceUid: "resource-1", IssuedAt: ts(0), AdmissionExpiresAt: ts(time.Minute), OperationDeadline: ts(time.Hour), Body: body}
		if name == "restore" {
			c.InstanceUid = "vm-2"
		}
		commands[name] = c
	}
	return commands
}
func sealed(t testing.TB, name string) *Command {
	t.Helper()
	c := commandFixtures()[name]
	if err := SealCommand(c); err != nil {
		t.Fatal(err)
	}
	return c
}
func receipt(c *Command) *AdmissionReceipt {
	return &AdmissionReceipt{MessageId: c.MessageId, OperationId: c.OperationId, InstanceUid: c.InstanceUid, ResourceUid: c.ResourceUid, IntentDigest: bytes.Clone(c.IntentDigest), AdmittedAt: ts(time.Second), State: OperationState_OPERATION_STATE_ADMITTED}
}
func requireCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var v *ValidationError
	if !errors.As(err, &v) || v.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestEveryCommandRoundTrip(t *testing.T) {
	fixtures := commandFixtures()
	if len(fixtures) != (&Command{}).ProtoReflect().Descriptor().Oneofs().ByName("body").Fields().Len() {
		t.Fatal("command fixture coverage is incomplete")
	}
	kinds := map[CommandKind]bool{}
	for name, c := range fixtures {
		t.Run(name, func(t *testing.T) {
			if err := SealCommand(c); err != nil {
				t.Fatal(err)
			}
			kind := Kind(c)
			if kind == CommandKind_COMMAND_KIND_UNSPECIFIED || kinds[kind] {
				t.Fatal("missing or duplicate kind")
			}
			kinds[kind] = true
			data, err := EncodeCommand(c)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeCommand(data)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(decoded, c) {
				t.Fatal("command changed in transit")
			}
			// The deadline is intent for every command, including safety/inspection.
			decoded.OperationDeadline = ts(2 * time.Hour)
			requireCode(t, ValidateCommand(decoded), ErrorCode_ERROR_CODE_OPERATION_CONFLICT)
		})
	}
}

func TestInvalidCommandVariants(t *testing.T) {
	tests := []struct {
		name, fixture string
		change        func(*Command)
	}{
		{"repository identity", "validate", func(c *Command) { c.GetValidateRepository().Repository.RepositoryUid = "" }},
		{"endpoint credentials", "validate", func(c *Command) {
			c.GetValidateRepository().Repository.Endpoint = "https://user:password@s3.example.test"
		}},
		{"plaintext endpoint", "validate", func(c *Command) { c.GetValidateRepository().Repository.Endpoint = "http://s3.example.test" }},
		{"query secret", "validate", func(c *Command) {
			c.GetValidateRepository().Repository.Endpoint = "https://s3.example.test?secret=value"
		}},
		{"prefix traversal", "validate", func(c *Command) { c.GetValidateRepository().Repository.Prefix = "tenant/../other" }},
		{"empty prefix", "validate", func(c *Command) { c.GetValidateRepository().Repository.Prefix = "/" }},
		{"credential version", "validate", func(c *Command) { c.GetValidateRepository().Credential.Version = "" }},
		{"credential algorithm", "validate", func(c *Command) { c.GetValidateRepository().Credential.Algorithm = "custom-cipher" }},
		{"missing ciphertext", "validate", func(c *Command) { c.GetValidateRepository().Credential.Ciphertext = nil }},
		{"zero retention", "policy", func(c *Command) { c.GetApplyBackupPolicy().RetentionDays = 0 }},
		{"large retention", "policy", func(c *Command) { c.GetApplyBackupPolicy().RetentionDays = 36 }},
		{"invalid window", "policy", func(c *Command) { c.GetApplyBackupPolicy().PreferredWindowUtc = "25:00-26:00" }},
		{"empty window", "policy", func(c *Command) { c.GetApplyBackupPolicy().PreferredWindowUtc = "01:00-01:00" }},
		{"unknown backup type", "backup", func(c *Command) { c.GetRunBackup().Type = 99 }},
		{"initial differential", "backup", func(c *Command) { c.GetRunBackup().Type = BackupType_BACKUP_TYPE_DIFFERENTIAL }},
		{"snapshot owner reuse", "snapshot", func(c *Command) { c.GetCreateSnapshot().SnapshotUid = "recreated-resource" }},
		{"disable generation", "disable", func(c *Command) { c.GetDisableAutomatedBackup().PolicyGeneration = 0 }},
		{"delete owner", "delete", func(c *Command) { c.GetDeleteAutomatedRecoveryData().DeletingResourceUid = "other" }},
		{"hold generation", "acquire", func(c *Command) { c.GetAcquireRestoreHold().Hold.Generation = 0 }},
		{"hold source binding", "acquire", func(c *Command) { c.GetAcquireRestoreHold().Hold.Source.Repository.SourceInstanceUid = "wrong-vm" }},
		{"release evidence", "release", func(c *Command) { c.GetReleaseRestoreHold().EvidenceOperationId = "" }},
		{"release reason", "release", func(c *Command) { c.GetReleaseRestoreHold().Reason = 0 }},
		{"restore latest selector", "restore", func(c *Command) { c.GetRestoreDatabase().Source.RecoveryTarget = nil }},
		{"restore lineage", "restore", func(c *Command) { c.GetRestoreDatabase().Source.SystemIdentifier = "" }},
		{"restore volume", "restore", func(c *Command) { c.GetRestoreDatabase().TargetVolumeUid = "" }},
		{"restore target", "restore", func(c *Command) { c.GetRestoreDatabase().TargetInstanceUid = "vm-3" }},
		{"self cancellation", "cancel", func(c *Command) { c.GetCancelOperation().TargetOperationId = c.OperationId }},
		{"duplicate section", "inspect", func(c *Command) { c.GetInspectState().Sections = []StateSection{1, 1} }},
		{"no sections", "inspect", func(c *Command) { c.GetInspectState().Sections = nil }},
		{"nil body", "backup", func(c *Command) { c.Body = nil }},
		{"nil command payload", "backup", func(c *Command) { c.Body = &Command_RunBackup{} }},
		{"typed nil wrapper", "backup", func(c *Command) { c.Body = (*Command_RunBackup)(nil) }},
		{"bad timestamp", "backup", func(c *Command) { c.IssuedAt = &timestamppb.Timestamp{Nanos: -1} }},
		{"expiry after deadline", "backup", func(c *Command) { c.AdmissionExpiresAt = ts(2 * time.Hour) }},
		{"unsafe identity", "backup", func(c *Command) { c.OperationId = "../operation" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := commandFixtures()[tt.fixture]
			tt.change(c)
			if err := SealCommand(c); err == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
	c := commandFixtures()["backup"]
	c.ProtocolMajor = 2
	requireCode(t, SealCommand(c), ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH)
	c = commandFixtures()["restore"]
	c.GetRestoreDatabase().Source.RecoveryTarget = &RestoreSource_Snapshot{Snapshot: &SnapshotSource{SnapshotUid: "snapshot-1", Type: SnapshotType_SNAPSHOT_TYPE_FINAL}}
	if err := SealCommand(c); err != nil {
		t.Fatalf("frozen snapshot selector: %v", err)
	}
}

func TestIntentCanonicalization(t *testing.T) {
	c := sealed(t, "policy")
	before := proto.Clone(c)
	digest, err := IntentDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(c, before) {
		t.Fatal("digest mutated the command")
	}
	// Fixed vector prevents a serializer/refactor from silently changing v1 intent.
	const expected = "cd2fdc43a0cc86fc86c7412e196b2b77e3b97aad976208317425e569bfa4e794"
	if got := hex.EncodeToString(digest); got != expected {
		t.Errorf("intent digest = %s", got)
	}
	copy := proto.Clone(c).(*Command)
	copy.MessageId = "different-message"
	copy.OperationId = "different-operation"
	copy.IssuedAt = ts(time.Second)
	copy.AdmissionExpiresAt = ts(2 * time.Minute)
	copy.GetApplyBackupPolicy().Credential.Ciphertext = []byte("different randomized ciphertext")
	copy.GetApplyBackupPolicy().Credential.KeyId = "rotated-key"
	copy.GetApplyBackupPolicy().Repository.Endpoint = "https://S3.EXAMPLE.TEST:443/"
	copy.GetApplyBackupPolicy().Repository.Prefix = "/tenant/instance/"
	got, err := IntentDigest(copy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, digest) {
		t.Fatal("transport, encryption or normalized storage changed intent")
	}
	for _, mutate := range []func(*Command){
		func(c *Command) { c.GetApplyBackupPolicy().Credential.Version = "43" },
		func(c *Command) { c.GetApplyBackupPolicy().Credential.CredentialUid = "recreated-secret" },
		func(c *Command) { c.GetApplyBackupPolicy().PolicyGeneration++ },
		func(c *Command) { c.GetApplyBackupPolicy().Repository.RepositoryUid = "recreated-repo" },
		func(c *Command) { c.GetApplyBackupPolicy().Repository.Prefix = "tenant/other" },
		func(c *Command) { c.ResourceUid = "recreated-owner" },
	} {
		changed := proto.Clone(c).(*Command)
		mutate(changed)
		got, err := IntentDigest(changed)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(got, digest) {
			t.Fatal("changed intent retained digest")
		}
	}
	inspect := sealed(t, "inspect")
	original := bytes.Clone(inspect.IntentDigest)
	inspect.GetInspectState().Sections = []StateSection{StateSection_STATE_SECTION_POLICY, StateSection_STATE_SECTION_SAFETY}
	if err := SealCommand(inspect); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(inspect.IntentDigest, original) {
		t.Fatal("section set order changed intent")
	}
}

func TestDuplicateIdentityAndAdmissionExpiry(t *testing.T) {
	c := sealed(t, "policy")
	r := receipt(c)
	if err := ValidateTarget(c, "vm-1", "resource-1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNewAdmission(c, testTime); err != nil {
		t.Fatal(err)
	}
	requireCode(t, ValidateTarget(c, "recreated-vm", "resource-1"), ErrorCode_ERROR_CODE_IDENTITY_MISMATCH)
	requireCode(t, ValidateNewAdmission(c, testTime.Add(time.Minute)), ErrorCode_ERROR_CODE_ADMISSION_EXPIRED)
	requireCode(t, ValidateNewAdmission(c, testTime.Add(time.Hour)), ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED)
	if err := MatchReceipt(c, r); err != nil {
		t.Fatalf("matching accepted receipt must survive expiry: %v", err)
	}
	for _, change := range []func(*Command){
		func(c *Command) { c.MessageId = "new-message" }, func(c *Command) { c.OperationId = "new-op" },
		func(c *Command) { c.ResourceUid = "new-owner" }, func(c *Command) { c.GetApplyBackupPolicy().RetentionDays = 8 },
	} {
		copy := proto.Clone(c).(*Command)
		change(copy)
		if err := SealCommand(copy); err != nil {
			t.Fatal(err)
		}
		requireCode(t, MatchReceipt(copy, r), ErrorCode_ERROR_CODE_OPERATION_CONFLICT)
	}
}

func TestEncodedSizeBoundary(t *testing.T) {
	c := sealed(t, "validate")
	cred := c.GetValidateRepository().Credential
	for proto.Size(c) != MaxEncodedSize {
		cred.Ciphertext = make([]byte, len(cred.Ciphertext)+MaxEncodedSize-proto.Size(c))
	}
	if err := SealCommand(c); err != nil {
		t.Fatal(err)
	}
	data, err := EncodeCommand(c)
	if err != nil || len(data) != MaxEncodedSize {
		t.Fatalf("boundary encode: %d, %v", len(data), err)
	}
	if _, err := DecodeCommand(data); err != nil {
		t.Fatal(err)
	}
	local := &LocalRequest{ProtocolMajor: 1, RequestId: "request-1", InstanceUid: c.InstanceUid, Body: &LocalRequest_Submit{Submit: c}}
	frame, err := EncodeLocalRequest(local)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) <= MaxEncodedSize {
		t.Fatal("test must exercise local envelope overhead")
	}
	if _, err := DecodeLocalRequest(frame); err != nil {
		t.Fatal(err)
	}
	cred.Ciphertext = append(cred.Ciphertext, 0)
	if _, err := EncodeCommand(c); err == nil {
		t.Fatal("oversized command encoded")
	}
	if _, err := DecodeCommand(append(data, 0)); err == nil {
		t.Fatal("oversized command decoded")
	}
	if _, err := DecodeLocalRequest(make([]byte, MaxLocalEncodedSize+1)); err == nil {
		t.Fatal("oversized IPC accepted")
	}
}

func TestMalformedAndAmbiguousWire(t *testing.T) {
	c := sealed(t, "backup")
	data, _ := EncodeCommand(c)
	unknown := protowire.AppendTag(nil, 99, protowire.BytesType)
	unknown = protowire.AppendBytes(unknown, []byte("unrecognized"))
	duplicateID := protowire.AppendTag(nil, 2, protowire.BytesType)
	duplicateID = protowire.AppendString(duplicateID, "other-id")
	otherBody := protowire.AppendTag(nil, 24, protowire.BytesType)
	otherBody = protowire.AppendBytes(otherBody, []byte{8, 1})
	for name, b := range map[string][]byte{"empty": nil, "truncated": data[:len(data)-1], "bad varint": {0x80}, "unknown": append(bytes.Clone(data), unknown...), "duplicate id": append(bytes.Clone(data), duplicateID...), "duplicate oneof": append(bytes.Clone(data), otherBody...)} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCommand(b); err == nil {
				t.Fatal("malformed command accepted")
			}
		})
	}
	c.GetRunBackup().Repository.ProtoReflect().SetUnknown(unknown)
	if err := SealCommand(c); err == nil {
		t.Fatal("nested unknown field accepted")
	}
	// Reordering fields changes wire bytes, not the decoded semantic digest.
	var fields [][]byte
	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		size := n + protowire.ConsumeFieldValue(num, typ, b[n:])
		fields = append(fields, bytes.Clone(b[:size]))
		b = b[size:]
	}
	var reversed []byte
	for i := len(fields) - 1; i >= 0; i-- {
		reversed = append(reversed, fields[i]...)
	}
	if _, err := DecodeCommand(reversed); err != nil {
		t.Fatalf("valid reordered wire: %v", err)
	}
}

func TestCredentialRedaction(t *testing.T) {
	c := sealed(t, "validate")
	secret := "DO-NOT-LOG-THIS-SECRET"
	c.GetValidateRepository().Credential.Ciphertext = []byte(secret)
	c.GetValidateRepository().Repository.Endpoint = "https://user:" + secret + "@example.test"
	err := ValidateCommand(c)
	if err == nil {
		t.Fatal("credential-bearing endpoint accepted")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(CommandSummary(c), secret) {
		t.Fatal("credential leaked through logging boundary")
	}
	for code := ErrorCode_ERROR_CODE_INVALID_COMMAND; code <= ErrorCode_ERROR_CODE_INTERNAL; code++ {
		if err := safeError(NewSafeError(code)); err != nil {
			t.Fatal(err)
		}
	}
	e := NewSafeError(ErrorCode_ERROR_CODE_INTERNAL)
	e.Message = secret
	if err := safeError(e); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("untrusted error text accepted or leaked")
	}
}

func TestTaskIdentityContracts(t *testing.T) {
	task := &TaskPayload{SchemaVersion: 1, OperationId: "op-1", InstanceUid: "vm-1"}
	b, err := EncodeTask(task)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTask(b)
	if err != nil || !proto.Equal(task, got) {
		t.Fatalf("task roundtrip: %v", err)
	}
	safety := &SafetyTaskPayload{SchemaVersion: 1, OperationId: "op-1", InstanceUid: "vm-1", CleanupGeneration: 2}
	b, err = EncodeSafetyTask(safety)
	if err != nil {
		t.Fatal(err)
	}
	s, err := DecodeSafetyTask(b)
	if err != nil || !proto.Equal(safety, s) {
		t.Fatalf("safety roundtrip: %v", err)
	}
	if _, err := DecodeTask(b); err == nil {
		t.Fatal("safety generation silently discarded by ordinary task")
	}
	safety.CleanupGeneration = 0
	if _, err := EncodeSafetyTask(safety); err == nil {
		t.Fatal("missing cleanup generation accepted")
	}
	task.SchemaVersion = 2
	if _, err := EncodeTask(task); err == nil {
		t.Fatal("unsupported task schema accepted")
	}
	for _, m := range []proto.Message{task, safety} {
		md := m.ProtoReflect().Descriptor()
		for i := 0; i < md.Fields().Len(); i++ {
			name := string(md.Fields().Get(i).Name())
			if name != "schema_version" && name != "operation_id" && name != "instance_uid" && name != "cleanup_generation" {
				t.Fatalf("task contains non-identity field %s", name)
			}
		}
	}
}

func FuzzDecodeCommand(f *testing.F) {
	for _, c := range commandFixtures() {
		if err := SealCommand(c); err != nil {
			f.Fatal(err)
		}
		b, err := EncodeCommand(c)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte{0x80})
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := DecodeCommand(data)
		if err != nil {
			return
		}
		b, err := EncodeCommand(c)
		if err != nil {
			t.Fatal(err)
		}
		again, err := DecodeCommand(b)
		if err != nil || !proto.Equal(c, again) {
			t.Fatalf("accepted command cannot roundtrip: %v", err)
		}
	})
}
