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

// SessionControlProvider is implemented only by runtimes that can bind a stable
// native session reference to an authoritative computecloud Attempt generation.
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
