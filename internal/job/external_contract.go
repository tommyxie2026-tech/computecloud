package job

// ExternalExecutionRequest is a provider-neutral ingress contract.
// It intentionally does not expose Worker, Attempt, lease, generation,
// process or persistence implementation details.
type ExternalExecutionRequest struct {
	ExecutionID string              `json:"execution_id"`
	Workload    ExternalWorkload    `json:"workload"`
	Capability  ExternalCapability  `json:"capability,omitempty"`
	Input       ExternalInput       `json:"input"`
	Context     ExternalContext     `json:"context,omitempty"`
	Constraints ExternalConstraints `json:"constraints,omitempty"`
	Policy      ExternalPolicy      `json:"policy,omitempty"`
	TraceID     string              `json:"trace_id,omitempty"`
}

type ExternalWorkload struct { Type string `json:"type"` }
type ExternalCapability struct { Required []string `json:"required,omitempty"` }
type ExternalInput struct { Goal string `json:"goal"` }
type ExternalContext struct {
	Artifacts []string `json:"artifacts,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
}
type ExternalConstraints struct {
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty"`
	Isolation      string `json:"isolation,omitempty"`
}
type ExternalPolicy struct { Permissions []string `json:"permissions,omitempty"` }

type ExternalExecutionPhase string

const (
	ExternalQueued       ExternalExecutionPhase = "QUEUED"
	ExternalAssigned     ExternalExecutionPhase = "ASSIGNED"
	ExternalStarted      ExternalExecutionPhase = "STARTED"
	ExternalWaitingInput ExternalExecutionPhase = "WAITING_INPUT"
	ExternalCompleted    ExternalExecutionPhase = "COMPLETED"
	ExternalFailed       ExternalExecutionPhase = "FAILED"
	ExternalCancelled    ExternalExecutionPhase = "CANCELLED"
)

type ExternalArtifactRef struct {
	URI      string `json:"uri"`
	Checksum string `json:"checksum,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

type ExternalExecutionResult struct {
	ExecutionID string                 `json:"execution_id"`
	Phase       ExternalExecutionPhase `json:"phase"`
	Message     string                 `json:"message,omitempty"`
	Artifacts   []ExternalArtifactRef  `json:"artifacts,omitempty"`
	TraceID     string                 `json:"trace_id,omitempty"`
}

// ValidateExternalExecutionRequest keeps validation at the public boundary
// without coupling callers to computecloud's internal Job/Task/Attempt model.
func ValidateExternalExecutionRequest(r ExternalExecutionRequest) error {
	if r.ExecutionID == "" { return ErrExternalExecutionIDRequired }
	if r.Workload.Type != "agent" { return ErrExternalAgentWorkloadRequired }
	if r.Input.Goal == "" { return ErrExternalGoalRequired }
	return nil
}
