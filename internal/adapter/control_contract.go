package adapter

import (
	"context"
	"errors"

	"github.com/tommyxie2026-tech/computecloud/internal/control"
)

var ErrControlCapabilityUnsupported = errors.New("control capability unsupported")

type ControlDescriptor struct {
	ProtocolVersion string
	Capabilities    []control.Capability
}

func (d ControlDescriptor) Validate() error {
	if d.ProtocolVersion != control.ProtocolV1Alpha1 {
		return errors.New("unsupported control protocol version")
	}
	return control.ValidateCapabilities(d.Capabilities)
}

// ControlProvider is an optional extension to Provider. Runtime execution does not
// require it; interactive control surfaces must type-assert this contract before
// exposing Session/Input/Approval features.
type ControlProvider interface {
	Provider
	ControlDescriptor() ControlDescriptor
}


func builtinControlDescriptor(p Provider) ControlDescriptor {
	caps := p.Capabilities()
	controlCaps := make([]control.Capability, 0, 3)
	hasRuntime := func(want string) bool {
		for _, capability := range caps.Runtime {
			if capability == want {
				return true
			}
		}
		return false
	}
	if hasRuntime("event_stream") {
		controlCaps = append(controlCaps, control.CapabilityStreamOutput, control.CapabilityStructuredOutput)
	}
	if hasRuntime("cancel") {
		controlCaps = append(controlCaps, control.CapabilityCancel)
	}
	normalized, err := control.NormalizeCapabilities(controlCaps)
	if err != nil {
		panic(err)
	}
	return ControlDescriptor{ProtocolVersion: control.ProtocolV1Alpha1, Capabilities: normalized}
}

func (p codexProvider) ControlDescriptor() ControlDescriptor  { return builtinControlDescriptor(p) }
func (p claudeProvider) ControlDescriptor() ControlDescriptor { return builtinControlDescriptor(p) }
func (p geminiProvider) ControlDescriptor() ControlDescriptor { return builtinControlDescriptor(p) }

// SessionControlProvider is implemented only by runtimes that can bind a stable
// native session reference to an authoritative computecloud Attempt generation.
// Resume is a current-Attempt transport/session rebind operation. It does not
// reopen a released Attempt, create a retry, or continue a terminal Job.
type SessionControlProvider interface {
	ControlProvider
	Resume(context.Context, ControlResumeRequest) (ExecutionRef, error)
	Input(context.Context, ControlInputRequest) error
	Approve(context.Context, ControlApprovalRequest) error
	Interrupt(context.Context, ControlInterruptRequest) error
}

type ControlResumeRequest struct {
	Ref        ExecutionRef
	SessionRef string
	AttemptID  string
	Generation int64
}

type ControlInputRequest struct {
	Ref        ExecutionRef
	SessionRef string
	AttemptID  string
	Generation int64
	Mode       string
	Content    string
}

type ControlApprovalRequest struct {
	Ref            ExecutionRef
	SessionRef     string
	AttemptID      string
	Generation     int64
	ApprovalID     string
	RequestVersion int64
	Decision       string
}

type ControlInterruptRequest struct {
	Ref        ExecutionRef
	SessionRef string
	AttemptID  string
	Generation int64
}

func ControlDescriptorFor(p Provider) (ControlDescriptor, bool, error) {
	cp, ok := p.(ControlProvider)
	if !ok {
		return ControlDescriptor{}, false, nil
	}
	desc := cp.ControlDescriptor()
	if err := desc.Validate(); err != nil {
		return ControlDescriptor{}, true, err
	}
	return desc, true, nil
}
