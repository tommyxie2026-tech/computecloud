package graph

import "github.com/tommyxie2026-tech/computecloud/internal/job"

// ReplayBinding is an administrator's pinned read-only environment contract.
// EnvironmentHash identifies independently audited tools/model deployment;
// RuntimeConfigHash and PolicyHash additionally bind actual Worker configuration.
type ReplayBinding struct {
	Version           string `json:"version" yaml:"version"`
	RuntimeProfile    string `json:"runtime_profile" yaml:"runtime_profile"`
	RuntimeVersion    string `json:"runtime_version" yaml:"runtime_version"`
	Model             string `json:"model" yaml:"model"`
	EnvironmentHash   string `json:"environment_sha256" yaml:"environment_sha256"`
	RuntimeConfigHash string `json:"runtime_config_sha256" yaml:"runtime_config_sha256"`
	PolicyHash        string `json:"policy_sha256" yaml:"policy_sha256"`
	ReadOnly          bool   `json:"read_only" yaml:"read_only"`
}

func (b ReplayBinding) Valid() bool {
	return job.Ref(b.Version) && job.Ref(b.RuntimeProfile) && b.RuntimeVersion != "" && b.Model != "" && job.ValidHash(b.EnvironmentHash) && job.ValidHash(b.RuntimeConfigHash) && job.ValidHash(b.PolicyHash) && b.ReadOnly
}
func (b ReplayBinding) Capability() string { return "iu_replay_" + Hash(canonical(b)) }

type ReuseKey struct {
	NodeHash      string        `json:"node_sha256"`
	ContextHash   string        `json:"context_sha256"`
	ReadViewHash  string        `json:"read_view_sha256"`
	OperatorHash  string        `json:"operator_sha256"`
	SchemaHash    string        `json:"schema_sha256"`
	TemplateHash  string        `json:"template_sha256"`
	WorkspaceHash string        `json:"workspace_sha256"`
	Binding       ReplayBinding `json:"binding"`
}

func (k ReuseKey) Valid() bool {
	return k.Binding.Valid() && job.ValidHash(k.NodeHash) && job.ValidHash(k.ContextHash) && job.ValidHash(k.ReadViewHash) && job.ValidHash(k.OperatorHash) && job.ValidHash(k.SchemaHash) && job.ValidHash(k.TemplateHash) && job.ValidHash(k.WorkspaceHash)
}
func (k ReuseKey) Digest() string { return Hash(canonical(k)) }

type ReuseDecision struct {
	Decision         string `json:"decision"`
	Reason           string `json:"reason"`
	SourceJob        string `json:"source_job,omitempty"`
	SourceNode       string `json:"source_node,omitempty"`
	SourceGeneration int64  `json:"source_generation,omitempty"`
	KeyHash          string `json:"key_sha256,omitempty"`
}

func DecideReuse(old, current ReuseKey, evidenceValid bool) ReuseDecision {
	if !old.Valid() || !current.Valid() {
		return ReuseDecision{Decision: "RECOMPUTE", Reason: "BINDING_UNPROVEN"}
	}
	if !evidenceValid {
		return ReuseDecision{Decision: "RECOMPUTE", Reason: "EVIDENCE_INVALID"}
	}
	if old.Digest() != current.Digest() {
		return ReuseDecision{Decision: "RECOMPUTE", Reason: "DEPENDENCY_CHANGED"}
	}
	return ReuseDecision{Decision: "REUSE", Reason: "EXACT_FROZEN_INPUT", KeyHash: current.Digest()}
}
