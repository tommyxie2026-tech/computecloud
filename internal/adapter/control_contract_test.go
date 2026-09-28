package adapter

import (
	"context"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
)

type controlFixtureProvider struct{ fixtureProvider }

func (controlFixtureProvider) Profile() string { return "control_fixture" }
func (controlFixtureProvider) ControlDescriptor() ControlDescriptor {
	return ControlDescriptor{
		ProtocolVersion: control.ProtocolV1Alpha1,
		Capabilities: []control.Capability{
			control.CapabilityStreamOutput,
			control.CapabilityCancel,
			control.CapabilitySessionResume,
		},
	}
}
func (controlFixtureProvider) Resume(context.Context, ControlResumeRequest) (ExecutionRef, error) {
	return ExecutionRef{Provider: "control_fixture", Transport: "remote_api", ID: "resumed"}, nil
}
func (controlFixtureProvider) Input(context.Context, ControlInputRequest) error { return nil }
func (controlFixtureProvider) Approve(context.Context, ControlApprovalRequest) error { return nil }
func (controlFixtureProvider) Interrupt(context.Context, ControlInterruptRequest) error { return nil }

// Keep compile-time proof that the control contract remains a strict optional
// extension of the existing runtime Provider contract.
var _ SessionControlProvider = controlFixtureProvider{}

func TestControlProviderContractHarness(t *testing.T) {
	p := controlFixtureProvider{}
	desc, ok, err := ControlDescriptorFor(p)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("control provider extension not detected")
	}
	if desc.ProtocolVersion != control.ProtocolV1Alpha1 {
		t.Fatalf("protocol=%q", desc.ProtocolVersion)
	}
	ref, err := p.Resume(context.Background(), ControlResumeRequest{
		Ref: ExecutionRef{Provider: p.Profile(), Transport: "remote_api", ID: "run1"},
		SessionRef: "native-session-1", AttemptID: "a1", Generation: 1,
	})
	if err != nil || ref.ID != "resumed" {
		t.Fatalf("resume ref=%+v err=%v", ref, err)
	}
}

func TestBuiltinProvidersDoNotClaimUnimplementedControlCapabilities(t *testing.T) {
	for _, profile := range []string{"codex_exec", "claude_print"} {
		p, ok := Lookup(profile)
		if !ok {
			t.Fatalf("missing builtin provider %s", profile)
		}
		if _, claimed, err := ControlDescriptorFor(p); err != nil || claimed {
			t.Fatalf("%s claimed interactive control before implementation: claimed=%v err=%v", profile, claimed, err)
		}
	}
}

// References keep this harness aligned with Provider's compile-time types.
var _ = (*pb.TaskSpec)(nil)
var _ = config.Runtime{}
