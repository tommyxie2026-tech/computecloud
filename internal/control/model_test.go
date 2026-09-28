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


func TestAgentControlOperationReceiptValidation(t *testing.T) {
	now := time.Now().UTC()
	receipt := OperationReceipt{
		ProtocolVersion: ProtocolV1Alpha1,
		OperationID: "op1",
		OperationType: "cancel",
		JobID: "j1",
		SessionID: "s1",
		TaskID: "t1",
		AttemptID: "a1",
		Generation: 1,
		State: OperationAccepted,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt.Generation = 0
	if err := receipt.Validate(); err == nil {
		t.Fatal("zero generation accepted")
	}
}
