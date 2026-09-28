package control

import (
	"errors"
	"fmt"
	"strings"
)

var knownEventTypes = map[string]struct{}{
	"session.created": {}, "session.started": {}, "session.resumed": {}, "session.interrupted": {},
	"session.completed": {}, "session.failed": {},
	"message.started": {}, "message.delta": {}, "message.completed": {}, "plan.updated": {},
	"tool.requested": {}, "tool.started": {}, "tool.completed": {}, "tool.failed": {},
	"approval.requested": {}, "approval.accepted": {}, "approval.rejected": {}, "approval.expired": {},
	"file.changed": {}, "diff.updated": {}, "artifact.created": {},
	"runtime.warning": {}, "runtime.disconnected": {}, "runtime.recovered": {},
	"control.accepted": {}, "control.completed": {}, "control.rejected": {},
}

var knownApprovalStates = map[ApprovalState]struct{}{
	ApprovalPending: {}, ApprovalAccepted: {}, ApprovalRejected: {}, ApprovalExpired: {}, ApprovalSuperseded: {},
}

var knownRiskClasses = map[string]struct{}{
	"LOW": {}, "MEDIUM": {}, "HIGH": {}, "CRITICAL": {},
}

func (e Event) Validate() error {
	if e.ProtocolVersion != ProtocolV1Alpha1 {
		return fmt.Errorf("unsupported control protocol: %q", e.ProtocolVersion)
	}
	for field, value := range map[string]string{
		"event_id": e.EventID, "job_id": e.JobID, "task_id": e.TaskID,
		"attempt_id": e.AttemptID, "type": e.Type,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s required", field)
		}
	}
	if e.Seq < 1 {
		return errors.New("seq must be positive")
	}
	if e.Generation < 1 {
		return errors.New("generation must be positive")
	}
	if _, ok := knownEventTypes[e.Type]; !ok {
		return fmt.Errorf("unknown control event type: %q", e.Type)
	}
	if e.OccurredAt.IsZero() {
		return errors.New("occurred_at required")
	}
	if e.Payload == nil {
		return errors.New("payload required")
	}
	return nil
}

func (a ApprovalRequest) Validate() error {
	if a.ProtocolVersion != ProtocolV1Alpha1 {
		return fmt.Errorf("unsupported control protocol: %q", a.ProtocolVersion)
	}
	for field, value := range map[string]string{
		"approval_id": a.ApprovalID, "job_id": a.JobID, "task_id": a.TaskID,
		"attempt_id": a.AttemptID, "session_id": a.SessionID, "tool": a.Tool,
		"action": a.Action, "risk_class": a.RiskClass,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s required", field)
		}
	}
	if a.Generation < 1 {
		return errors.New("generation must be positive")
	}
	if a.RequestVersion < 1 {
		return errors.New("request_version must be positive")
	}
	if a.RequestedAt.IsZero() {
		return errors.New("requested_at required")
	}
	if !a.ExpiresAt.IsZero() && !a.ExpiresAt.After(a.RequestedAt) {
		return errors.New("expires_at must be after requested_at")
	}
	if _, ok := knownRiskClasses[a.RiskClass]; !ok {
		return fmt.Errorf("unknown approval risk class: %q", a.RiskClass)
	}
	if _, ok := knownApprovalStates[a.State]; !ok {
		return fmt.Errorf("unknown approval state: %q", a.State)
	}
	return nil
}

func (o ControlOperation) Validate() error {
	for field, value := range map[string]string{
		"principal_id": o.PrincipalID, "operation_id": o.OperationID,
		"operation_type": o.OperationType, "resource_type": o.ResourceType,
		"resource_id": o.ResourceID, "request_hash": o.RequestHash, "state": o.State,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s required", field)
		}
	}
	if o.ExpectedAttemptID != "" && o.ExpectedGeneration < 1 {
		return errors.New("expected_generation must be positive for attempt-scoped control")
	}
	if o.ExpectedResourceVersion < 0 {
		return errors.New("expected_resource_version must not be negative")
	}
	return nil
}
