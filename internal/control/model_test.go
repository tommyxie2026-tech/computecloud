package control

import (
	"encoding/json"
	"testing"
)

func TestAgentControlSessionValidation(t *testing.T) {
	session := AgentSession{
		ProtocolVersion: ProtocolV1Alpha1,
		SessionID: "s1", JobID: "j1", TaskID: "t1", AttemptID: "a1",
		Generation: 1, Runtime: "fixture",
		State: SessionRunning,
		Capabilities: []Capability{CapabilityCancel, CapabilityStreamOutput},
	}
	if err := session.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(session); err != nil {
		t.Fatal(err)
	}
}

func TestAgentControlRejectsUnknownOrDuplicateCapability(t *testing.T) {
	if err := ValidateCapabilities([]Capability{"vendor_private"}); err == nil {
		t.Fatal("unknown capability accepted")
	}
	if err := ValidateCapabilities([]Capability{CapabilityCancel, CapabilityCancel}); err == nil {
		t.Fatal("duplicate capability accepted")
	}
}

func TestAgentControlProtocolNegotiation(t *testing.T) {
	got, err := Negotiate(
		VersionRange{Min: ProtocolV1Alpha1, Max: ProtocolV1Alpha1},
		VersionRange{Min: ProtocolV1Alpha1, Max: ProtocolV1Alpha1},
	)
	if err != nil || got != ProtocolV1Alpha1 {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := Negotiate(
		VersionRange{Min: "control.v0", Max: "control.v0"},
		VersionRange{Min: ProtocolV1Alpha1, Max: ProtocolV1Alpha1},
	); err == nil {
		t.Fatal("incompatible version accepted")
	}
}

func TestNormalizeCapabilitiesIsStable(t *testing.T) {
	got, err := NormalizeCapabilities([]Capability{CapabilityUsage, CapabilityCancel})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != CapabilityCancel || got[1] != CapabilityUsage {
		t.Fatalf("unexpected order: %v", got)
	}
}


func TestAgentControlRejectsUnknownSessionState(t *testing.T) {
	session := AgentSession{
		ProtocolVersion: ProtocolV1Alpha1,
		SessionID: "s1", JobID: "j1", TaskID: "t1", AttemptID: "a1",
		Generation: 1, Runtime: "fixture", State: SessionState("VENDOR_PRIVATE"),
	}
	if err := session.Validate(); err == nil {
		t.Fatal("unknown session state accepted")
	}
}

func TestAgentControlJSONRoundTrip(t *testing.T) {
	session := AgentSession{
		ProtocolVersion: ProtocolV1Alpha1,
		SessionID: "s1", JobID: "j1", TaskID: "t1", AttemptID: "a1",
		Generation: 2, WorkerID: "w1", Runtime: "fixture", RuntimeVersion: "1.0",
		RuntimeSessionRef: "native-1", State: SessionWaitingApproval,
		Capabilities: []Capability{CapabilityApproval, CapabilityStreamOutput},
	}
	event := Event{
		ProtocolVersion: ProtocolV1Alpha1, EventID: "e1", Seq: 7,
		JobID: "j1", TaskID: "t1", AttemptID: "a1", Generation: 2,
		SessionID: "s1", Type: "approval.requested", Payload: map[string]any{"approval_id": "ap1"},
	}
	approval := ApprovalRequest{
		ProtocolVersion: ProtocolV1Alpha1, ApprovalID: "ap1", JobID: "j1", TaskID: "t1",
		AttemptID: "a1", Generation: 2, SessionID: "s1", Tool: "shell", Action: "execute",
		RiskClass: "HIGH", RequestVersion: 1, State: ApprovalPending,
	}
	op := ControlOperation{
		PrincipalID: "owner", OperationID: "op1", OperationType: "approval",
		ResourceType: "approval", ResourceID: "ap1", RequestHash: "hash",
		ExpectedAttemptID: "a1", ExpectedGeneration: 2, ExpectedResourceVersion: 3,
		State: "ACCEPTED",
	}
	for name, value := range map[string]any{
		"session": session, "event": event, "approval": approval, "operation": op,
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		var decoded map[string]any
		if err = json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s unmarshal: %v", name, err)
		}
		if name != "operation" && decoded["protocol_version"] != ProtocolV1Alpha1 {
			t.Fatalf("%s protocol_version=%v", name, decoded["protocol_version"])
		}
		if name == "session" {
			var got AgentSession
			if err = json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if err = got.Validate(); err != nil {
				t.Fatalf("round-trip session invalid: %v", err)
			}
		}
	}
}
