package guestprotocolv1

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

func testHealth() *BackupHealth {
	return &BackupHealth{ObservedAt: ts(0), Scheduler: HealthState_HEALTH_STATE_DISABLED, Repository: HealthState_HEALTH_STATE_UNKNOWN, Archiver: HealthState_HEALTH_STATE_DISABLED, Pitr: HealthState_HEALTH_STATE_DISABLED}
}
func testArtifact() *BackupArtifact {
	return &BackupArtifact{Repository: testRepo(), BackupLabel: "20260909-090000F", CompletedAt: ts(-time.Minute), PostgresMajor: 17, SystemIdentifier: "123456789", Timeline: 1}
}
func testHello() *AgentHello {
	return &AgentHello{ProtocolMajors: []uint32{1}, ExecutorVersion: "0.1.0", TaskSchemaVersion: 1}
}
func event(body isEvent_Body) *Event {
	e := &Event{ProtocolMajor: 1, EventId: "event-1", InstanceUid: "vm-1", ResourceUid: "resource-1", OccurredAt: ts(time.Second), AgentVersion: "0.1.0", Body: body}
	switch body.(type) {
	case *Event_Admitted, *Event_OperationStatus:
		e.OperationId = "operation-1"
		e.CommandMessageId = "message-1"
		e.EventSequence = 1
	case *Event_Rejected:
		e.OperationId = "operation-1"
		e.CommandMessageId = "message-1"
	}
	return e
}
func TestEveryEventRoundTrip(t *testing.T) {
	bodies := []isEvent_Body{
		&Event_Hello{Hello: testHello()}, &Event_Admitted{Admitted: receipt(sealed(t, "backup"))},
		&Event_Rejected{Rejected: NewSafeError(ErrorCode_ERROR_CODE_ADMISSION_EXPIRED)},
		&Event_OperationStatus{OperationStatus: &OperationStatus{State: OperationState_OPERATION_STATE_RUNNING, Attempt: 1}},
		&Event_BackupHealth{BackupHealth: testHealth()}, &Event_Heartbeat{Heartbeat: &AgentHeartbeat{UptimeSeconds: 10}},
	}
	if len(bodies) != (&Event{}).ProtoReflect().Descriptor().Oneofs().ByName("body").Fields().Len() {
		t.Fatal("missing event fixture")
	}
	for _, body := range bodies {
		e := event(body)
		b, err := EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeEvent(b)
		if err != nil || !proto.Equal(e, decoded) {
			t.Fatalf("event roundtrip: %v", err)
		}
	}
}

func TestEveryOperationResult(t *testing.T) {
	results := []isOperationStatus_Result{
		&OperationStatus_RepositoryValidated{RepositoryValidated: &RepositoryValidated{Repository: testRepo(), CredentialVersion: "42"}},
		&OperationStatus_PolicyApplied{PolicyApplied: &PolicyApplied{PolicyGeneration: 1, CredentialVersion: "42"}},
		&OperationStatus_Backup{Backup: testArtifact()},
		&OperationStatus_SnapshotCreated{SnapshotCreated: &SnapshotCreated{SnapshotUid: "snapshot-1", Type: SnapshotType_SNAPSHOT_TYPE_FINAL, Artifact: testArtifact()}},
		&OperationStatus_AutomationDisabled{AutomationDisabled: &AutomationDisabled{PolicyGeneration: 2}},
		&OperationStatus_AutomatedDataDeleted{AutomatedDataDeleted: &AutomatedDataDeleted{RepositoryUid: "repo-1", DeletionGeneration: 1}},
		&OperationStatus_HoldAcknowledged{HoldAcknowledged: &HoldAcknowledged{HoldId: "hold-1", Generation: 1}},
		&OperationStatus_RestoreCompleted{RestoreCompleted: &RestoreCompleted{Source: testSource(), TargetVolumeUid: "volume-2", RecoveryEnded: true, ManagedCredentialsReplaced: true, Writable: true, SourceReadsFinished: true}},
		&OperationStatus_CancellationRecorded{CancellationRecorded: &CancellationRecorded{TargetOperationId: "other-op"}},
		&OperationStatus_Inspection{Inspection: &StateInspection{BackupHealth: testHealth(), Safety: &SafetyState{Holds: []*RestoreHold{testHold()}}, Operations: []*OperationSummary{{OperationId: "other-op", State: OperationState_OPERATION_STATE_RETRY_WAIT, LastEventSequence: 2}}}},
	}
	if len(results) != (&OperationStatus{}).ProtoReflect().Descriptor().Oneofs().ByName("result").Fields().Len() {
		t.Fatal("missing result fixture")
	}
	for _, r := range results {
		e := event(&Event_OperationStatus{OperationStatus: &OperationStatus{State: OperationState_OPERATION_STATE_SUCCEEDED, Attempt: 1, Result: r}})
		b, err := EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeEvent(b)
		if err != nil || !proto.Equal(e, decoded) {
			t.Fatalf("result roundtrip: %v", err)
		}
	}
	for state := OperationState_OPERATION_STATE_ADMITTED; state <= OperationState_OPERATION_STATE_OUTCOME_UNKNOWN; state++ {
		status := &OperationStatus{State: state}
		switch state {
		case OperationState_OPERATION_STATE_SUCCEEDED:
			status.Result = results[0]
		case OperationState_OPERATION_STATE_RETRY_WAIT:
			status.Error = NewSafeError(ErrorCode_ERROR_CODE_PREREQUISITE_PENDING)
		case OperationState_OPERATION_STATE_FAILED:
			status.Error = NewSafeError(ErrorCode_ERROR_CODE_INTERNAL)
		case OperationState_OPERATION_STATE_OUTCOME_UNKNOWN:
			status.Error = NewSafeError(ErrorCode_ERROR_CODE_OUTCOME_UNKNOWN)
		}
		if _, err := EncodeEvent(event(&Event_OperationStatus{OperationStatus: status})); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventValidation(t *testing.T) {
	tests := map[string]*Event{
		"no success evidence": event(&Event_OperationStatus{OperationStatus: &OperationStatus{State: OperationState_OPERATION_STATE_SUCCEEDED}}),
		"no failure reason":   event(&Event_OperationStatus{OperationStatus: &OperationStatus{State: OperationState_OPERATION_STATE_FAILED}}),
		"unknown state":       event(&Event_OperationStatus{OperationStatus: &OperationStatus{State: 99}}),
		"missing body":        event(nil),
		"nil payload":         event(&Event_Heartbeat{}),
		"typed nil wrapper":   event((*Event_Heartbeat)(nil)),
		"untrusted error":     event(&Event_Rejected{Rejected: &SafeError{Code: ErrorCode_ERROR_CODE_INTERNAL, Message: "password=secret"}}),
	}
	wrongReceipt := event(&Event_Admitted{Admitted: receipt(sealed(t, "backup"))})
	wrongReceipt.GetAdmitted().InstanceUid = "wrong-vm"
	tests["receipt binding"] = wrongReceipt
	zeroSequence := event(&Event_OperationStatus{OperationStatus: &OperationStatus{State: OperationState_OPERATION_STATE_RUNNING}})
	zeroSequence.EventSequence = 0
	tests["missing sequence"] = zeroSequence
	wrongRejection := event(&Event_Rejected{Rejected: NewSafeError(ErrorCode_ERROR_CODE_INVALID_COMMAND)})
	wrongRejection.EventSequence = 1
	tests["rejection advancing operation"] = wrongRejection
	heartbeat := event(&Event_Heartbeat{Heartbeat: &AgentHeartbeat{}})
	heartbeat.OperationId = "not-liveness"
	tests["heartbeat operation claim"] = heartbeat
	hello := testHello()
	hello.Capabilities = []CommandKind{1, 1}
	tests["duplicate capability"] = event(&Event_Hello{Hello: hello})
	hello = testHello()
	hello.Capabilities = []CommandKind{99}
	tests["unknown capability"] = event(&Event_Hello{Hello: hello})
	for name, e := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodeEvent(e); err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
	if err := capabilities(testHello()); err != nil {
		t.Fatal("an empty capability set must be allowed before handlers exist")
	}
}

func TestRecoveryHealthEvidence(t *testing.T) {
	h := testHealth()
	h.AutomationEnabled = true
	h.AppliedPolicyGeneration = 1
	h.AppliedCredentialVersion = "42"
	h.RepositoryIdentity = testRepo()
	h.InitialBackupComplete = true
	h.Pitr = HealthState_HEALTH_STATE_HEALTHY
	h.RecoveryBounds = &RecoveryBounds{EarliestRestorableTime: ts(-time.Hour), LatestRestorableTime: ts(-time.Minute), VerifiedAt: ts(0), SystemIdentifier: "123456789", Timeline: 1, BaseBackupLabel: "20260909-090000F"}
	if _, err := EncodeEvent(event(&Event_BackupHealth{BackupHealth: h})); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BackupHealth){
		func(h *BackupHealth) { h.RecoveryBounds = nil }, func(h *BackupHealth) { h.RecoveryBounds.LatestRestorableTime = ts(time.Hour) },
		func(h *BackupHealth) { h.RecoveryBounds.EarliestRestorableTime = ts(time.Hour) }, func(h *BackupHealth) { h.RecoveryBounds.Timeline = 0 },
		func(h *BackupHealth) { h.InitialBackupComplete = false }, func(h *BackupHealth) { h.AutomationEnabled = false },
	} {
		copy := proto.Clone(h).(*BackupHealth)
		change(copy)
		if err := health(copy); err == nil {
			t.Fatal("unsupported healthy PITR claim accepted")
		}
	}
}

func TestLocalIPCRoundTrips(t *testing.T) {
	requests := []isLocalRequest_Body{
		&LocalRequest_Submit{Submit: sealed(t, "backup")}, &LocalRequest_Inspect{Inspect: &InspectState{Sections: []StateSection{StateSection_STATE_SECTION_POLICY}}},
		&LocalRequest_ReadPendingEvents{ReadPendingEvents: &ReadPendingEvents{Limit: 64}}, &LocalRequest_AcknowledgeEvents{AcknowledgeEvents: &AcknowledgeEvents{EventIds: []string{"event-1"}}}, &LocalRequest_GetCapabilities{GetCapabilities: &GetCapabilities{}},
	}
	if len(requests) != (&LocalRequest{}).ProtoReflect().Descriptor().Oneofs().ByName("body").Fields().Len() {
		t.Fatal("missing IPC request fixture")
	}
	for _, body := range requests {
		r := &LocalRequest{ProtocolMajor: 1, RequestId: "request-1", InstanceUid: "vm-1", Body: body}
		b, err := EncodeLocalRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeLocalRequest(b)
		if err != nil || !proto.Equal(r, got) {
			t.Fatalf("IPC request: %v", err)
		}
	}
	responses := []isLocalResponse_Body{
		&LocalResponse_Submission{Submission: &SubmitResponse{ProtocolMajor: 1, Outcome: &SubmitResponse_Receipt{Receipt: receipt(sealed(t, "backup"))}}},
		&LocalResponse_Inspection{Inspection: &StateInspection{BackupHealth: testHealth()}},
		&LocalResponse_PendingEvents{PendingEvents: &PendingEvents{Events: []*Event{event(&Event_Heartbeat{Heartbeat: &AgentHeartbeat{}})}}},
		&LocalResponse_EventsAcknowledged{EventsAcknowledged: &EventsAcknowledged{EventIds: []string{"event-1"}}},
		&LocalResponse_Capabilities{Capabilities: testHello()}, &LocalResponse_Error{Error: NewSafeError(ErrorCode_ERROR_CODE_INTERNAL)},
	}
	if len(responses) != (&LocalResponse{}).ProtoReflect().Descriptor().Oneofs().ByName("body").Fields().Len() {
		t.Fatal("missing IPC response fixture")
	}
	responses = append(responses, &LocalResponse_Submission{Submission: &SubmitResponse{ProtocolMajor: 1, Outcome: &SubmitResponse_Rejection{Rejection: NewSafeError(ErrorCode_ERROR_CODE_BACKLOG_FULL)}}})
	for _, body := range responses {
		r := &LocalResponse{ProtocolMajor: 1, RequestId: "request-1", Body: body}
		b, err := EncodeLocalResponse(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeLocalResponse(b)
		if err != nil || !proto.Equal(r, got) {
			t.Fatalf("IPC response: %v", err)
		}
	}
	bad := &LocalRequest{ProtocolMajor: 1, RequestId: "request-1", InstanceUid: "wrong-vm", Body: requests[0]}
	if _, err := EncodeLocalRequest(bad); err == nil {
		t.Fatal("IPC target mismatch accepted")
	}
	if err := eventIDs([]string{"event-1", "event-1"}); err == nil {
		t.Fatal("duplicate publication acknowledgement accepted")
	}
	if err := eventIDs(make([]string, 65)); err == nil {
		t.Fatal("unbounded acknowledgement batch accepted")
	}
}
