package control

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

func roundTrip[T any](t *testing.T, in T) T {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAgentControlRoundTripModels(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	session := AgentSession{
		ProtocolVersion: ProtocolV1Alpha1,
		SessionID: "s1", JobID: "j1", TaskID: "t1", AttemptID: "a1",
		Generation: 1, Runtime: "fixture", State: SessionRunning,
		Capabilities: []Capability{CapabilityCancel, CapabilityStreamOutput},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := session.Validate(); err != nil {
		t.Fatal(err)
	}
	if out := roundTrip(t, session); !reflect.DeepEqual(session, out) {
		t.Fatalf("session round-trip mismatch: %#v %#v", session, out)
	}

	event := Event{
		ProtocolVersion: ProtocolV1Alpha1,
		EventID: "e1", Seq: 1, JobID: "j1", TaskID: "t1", AttemptID: "a1",
		Generation: 1, SessionID: "s1", Type: "message.completed",
		OccurredAt: now, Payload: map[string]any{"text": "done"},
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	outEvent := roundTrip(t, event)
	if err := outEvent.Validate(); err != nil {
		t.Fatal(err)
	}

	approval := ApprovalRequest{
		ProtocolVersion: ProtocolV1Alpha1,
		ApprovalID: "p1", JobID: "j1", TaskID: "t1", AttemptID: "a1",
		Generation: 1, SessionID: "s1", Tool: "shell", Action: "run",
		RiskClass: "HIGH", RequestVersion: 1, RequestedAt: now,
		ExpiresAt: now.Add(time.Minute), State: ApprovalPending,
	}
	if err := approval.Validate(); err != nil {
		t.Fatal(err)
	}
	outApproval := roundTrip(t, approval)
	if err := outApproval.Validate(); err != nil {
		t.Fatal(err)
	}

	op := ControlOperation{
		PrincipalID: "owner", OperationID: "op1", OperationType: "input",
		ResourceType: "session", ResourceID: "s1", RequestHash: "hash",
		ExpectedAttemptID: "a1", ExpectedGeneration: 1, ExpectedResourceVersion: 1,
		State: "ACCEPTED",
	}
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	outOp := roundTrip(t, op)
	if err := outOp.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentControlSchemaEnumsStayAligned(t *testing.T) {
	readEnum := func(path string, keys ...string) []string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		var cur any = doc
		for _, key := range keys {
			cur = cur.(map[string]any)[key]
		}
		values := cur.([]any)
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = append(out, value.(string))
		}
		sort.Strings(out)
		return out
	}

	var modelEvents []string
	for value := range knownEventTypes {
		modelEvents = append(modelEvents, value)
	}
	sort.Strings(modelEvents)
	schemaEvents := readEnum("../../api/control/v1alpha1/event.schema.json", "properties", "type", "enum")
	if !reflect.DeepEqual(modelEvents, schemaEvents) {
		t.Fatalf("event enum drift\nmodel=%v\nschema=%v", modelEvents, schemaEvents)
	}

	var risk []string
	for value := range knownRiskClasses {
		risk = append(risk, value)
	}
	sort.Strings(risk)
	schemaRisk := readEnum("../../api/control/v1alpha1/approval.schema.json", "properties", "risk_class", "enum")
	if !reflect.DeepEqual(risk, schemaRisk) {
		t.Fatalf("risk enum drift model=%v schema=%v", risk, schemaRisk)
	}
}

func TestAgentControlErrorCodes(t *testing.T) {
	got := []string{
		ErrorCapabilityUnsupported.String(),
		ErrorAttemptFenced.String(),
		ErrorOperationConflict.String(),
		ErrorResourceVersionConflict.String(),
		ErrorEventCursorExpired.String(),
		ErrorExecutionUnverifiable.String(),
		ErrorPermissionBlocked.String(),
	}
	want := []string{
		"CAPABILITY_UNSUPPORTED",
		"ATTEMPT_FENCED",
		"OPERATION_CONFLICT",
		"RESOURCE_VERSION_CONFLICT",
		"EVENT_CURSOR_EXPIRED",
		"EXECUTION_UNVERIFIABLE",
		"PERMISSION_BLOCKED",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stable error codes changed: got=%v want=%v", got, want)
	}
}

func TestAgentControlNegativeValidation(t *testing.T) {
	now := time.Now().UTC()
	if err := (Event{ProtocolVersion: ProtocolV1Alpha1, EventID: "e", Seq: 1, JobID: "j", TaskID: "t", AttemptID: "a", Generation: 1, Type: "vendor.private", OccurredAt: now, Payload: map[string]any{}}).Validate(); err == nil {
		t.Fatal("unknown event type accepted")
	}
	if err := (ApprovalRequest{ProtocolVersion: ProtocolV1Alpha1, ApprovalID: "p", JobID: "j", TaskID: "t", AttemptID: "a", Generation: 1, SessionID: "s", Tool: "shell", Action: "run", RiskClass: "DANGEROUS", RequestVersion: 1, RequestedAt: now, State: ApprovalPending}).Validate(); err == nil {
		t.Fatal("unknown risk class accepted")
	}
	if err := (ControlOperation{PrincipalID: "p", OperationID: "o", OperationType: "input", ResourceType: "session", ResourceID: "s", RequestHash: "h", ExpectedAttemptID: "a", ExpectedGeneration: 0, State: "ACCEPTED"}).Validate(); err == nil {
		t.Fatal("attempt scoped control without generation accepted")
	}
}
