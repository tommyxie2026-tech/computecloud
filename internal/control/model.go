package control

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const ProtocolV1Alpha1 = "control.v1alpha1"

type Capability string

const (
	CapabilityStreamOutput     Capability = "stream_output"
	CapabilityStructuredOutput Capability = "structured_output"
	CapabilitySessionResume    Capability = "session_resume"
	CapabilityInteractiveInput Capability = "interactive_input"
	CapabilityQueueNextInput   Capability = "queue_next_input"
	CapabilitySteerCurrent     Capability = "steer_current"
	CapabilityApproval         Capability = "approval"
	CapabilityInterrupt        Capability = "interrupt"
	CapabilityCancel           Capability = "cancel"
	CapabilityPlan             Capability = "plan"
	CapabilityDiff             Capability = "diff"
	CapabilityFileRead         Capability = "file_read"
	CapabilityFileWrite        Capability = "file_write"
	CapabilityTerminal         Capability = "terminal"
	CapabilityToolEvents       Capability = "tool_events"
	CapabilityUsage            Capability = "usage"
	CapabilityArtifactExport   Capability = "artifact_export"
)

var knownCapabilities = map[Capability]struct{}{
	CapabilityStreamOutput: {}, CapabilityStructuredOutput: {}, CapabilitySessionResume: {},
	CapabilityInteractiveInput: {}, CapabilityQueueNextInput: {}, CapabilitySteerCurrent: {},
	CapabilityApproval: {}, CapabilityInterrupt: {}, CapabilityCancel: {}, CapabilityPlan: {},
	CapabilityDiff: {}, CapabilityFileRead: {}, CapabilityFileWrite: {}, CapabilityTerminal: {},
	CapabilityToolEvents: {}, CapabilityUsage: {}, CapabilityArtifactExport: {},
}

type SessionState string

const (
	SessionCreated         SessionState = "CREATED"
	SessionStarting        SessionState = "STARTING"
	SessionRunning         SessionState = "RUNNING"
	SessionWaitingInput    SessionState = "WAITING_INPUT"
	SessionWaitingApproval SessionState = "WAITING_APPROVAL"
	SessionInterrupting    SessionState = "INTERRUPTING"
	SessionCompleted       SessionState = "COMPLETED"
	SessionFailed          SessionState = "FAILED"
	SessionCanceled        SessionState = "CANCELED"
	SessionUnverifiable    SessionState = "UNVERIFIABLE"
)

type AgentSession struct {
	ProtocolVersion   string       `json:"protocol_version"`
	SessionID         string       `json:"session_id"`
	JobID             string       `json:"job_id"`
	TaskID            string       `json:"task_id"`
	AttemptID         string       `json:"attempt_id"`
	Generation        int64        `json:"generation"`
	WorkerID          string       `json:"worker_id,omitempty"`
	Runtime           string       `json:"runtime"`
	RuntimeVersion    string       `json:"runtime_version,omitempty"`
	RuntimeSessionRef string       `json:"runtime_session_ref,omitempty"`
	State             SessionState `json:"state"`
	Capabilities      []Capability `json:"capabilities"`
	CreatedAt         time.Time    `json:"created_at,omitempty"`
	UpdatedAt         time.Time    `json:"updated_at,omitempty"`
}

func validSessionState(state SessionState) bool {
	switch state {
	case SessionCreated, SessionStarting, SessionRunning, SessionWaitingInput,
		SessionWaitingApproval, SessionInterrupting, SessionCompleted, SessionFailed,
		SessionCanceled, SessionUnverifiable:
		return true
	default:
		return false
	}
}

func (s AgentSession) Validate() error {
	if s.ProtocolVersion != ProtocolV1Alpha1 {
		return fmt.Errorf("unsupported control protocol: %q", s.ProtocolVersion)
	}
	for field, value := range map[string]string{
		"session_id": s.SessionID, "job_id": s.JobID, "task_id": s.TaskID,
		"attempt_id": s.AttemptID, "runtime": s.Runtime,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s required", field)
		}
	}
	if s.Generation < 1 {
		return errors.New("generation must be positive")
	}
	if !validSessionState(s.State) {
		return fmt.Errorf("invalid session state: %q", s.State)
	}
	return ValidateCapabilities(s.Capabilities)
}

func ValidateCapabilities(in []Capability) error {
	seen := map[Capability]bool{}
	for _, capability := range in {
		if _, ok := knownCapabilities[capability]; !ok {
			return fmt.Errorf("unknown control capability: %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("duplicate control capability: %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

func NormalizeCapabilities(in []Capability) ([]Capability, error) {
	if err := ValidateCapabilities(in); err != nil {
		return nil, err
	}
	out := append([]Capability(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

type Event struct {
	ProtocolVersion string         `json:"protocol_version"`
	EventID         string         `json:"event_id"`
	Seq             int64          `json:"seq"`
	JobID           string         `json:"job_id"`
	TaskID          string         `json:"task_id"`
	AttemptID       string         `json:"attempt_id"`
	Generation      int64          `json:"generation"`
	SessionID       string         `json:"session_id,omitempty"`
	Type            string         `json:"type"`
	OccurredAt      time.Time      `json:"occurred_at"`
	Payload         map[string]any `json:"payload"`
}

type ApprovalState string

const (
	ApprovalPending    ApprovalState = "PENDING"
	ApprovalAccepted   ApprovalState = "ACCEPTED"
	ApprovalRejected   ApprovalState = "REJECTED"
	ApprovalExpired    ApprovalState = "EXPIRED"
	ApprovalSuperseded ApprovalState = "SUPERSEDED"
)

type ApprovalRequest struct {
	ProtocolVersion  string         `json:"protocol_version"`
	ApprovalID       string         `json:"approval_id"`
	JobID            string         `json:"job_id"`
	TaskID           string         `json:"task_id"`
	AttemptID        string         `json:"attempt_id"`
	Generation       int64          `json:"generation"`
	SessionID        string         `json:"session_id"`
	Tool             string         `json:"tool"`
	Action           string         `json:"action"`
	RiskClass        string         `json:"risk_class"`
	ArgumentsSummary string         `json:"arguments_summary,omitempty"`
	PolicyContext    map[string]any `json:"policy_context,omitempty"`
	RequestVersion   int64          `json:"request_version"`
	RequestedAt      time.Time      `json:"requested_at"`
	ExpiresAt        time.Time      `json:"expires_at,omitempty"`
	State            ApprovalState  `json:"state"`
}

type ControlOperation struct {
	PrincipalID            string `json:"principal_id"`
	OperationID            string `json:"operation_id"`
	OperationType          string `json:"operation_type"`
	ResourceType           string `json:"resource_type"`
	ResourceID             string `json:"resource_id"`
	RequestHash            string `json:"request_hash"`
	ExpectedAttemptID      string `json:"expected_attempt_id,omitempty"`
	ExpectedGeneration     int64  `json:"expected_generation,omitempty"`
	ExpectedResourceVersion int64 `json:"expected_resource_version,omitempty"`
	State                  string `json:"state"`
	Receipt                any    `json:"receipt,omitempty"`
}

type VersionRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

func Negotiate(client, server VersionRange) (string, error) {
	if client.Min == "" || client.Max == "" || server.Min == "" || server.Max == "" {
		return "", errors.New("protocol version range is incomplete")
	}
	if client.Min != ProtocolV1Alpha1 || client.Max != ProtocolV1Alpha1 ||
		server.Min != ProtocolV1Alpha1 || server.Max != ProtocolV1Alpha1 {
		return "", errors.New("no compatible control protocol version")
	}
	return ProtocolV1Alpha1, nil
}
