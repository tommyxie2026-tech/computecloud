package iuexperiment

import "fmt"

// IncrementalArm contains observed execution facts, never inferred savings.
// Unknown Provider usage and fees remain nil, even when fixture call counts exist.
type IncrementalArm struct {
	DatasetHash             string   `json:"dataset_sha256"`
	OutputHash              string   `json:"output_sha256"`
	BusinessVerdict         string   `json:"business_verdict"`
	Tasks                   int      `json:"tasks"`
	Attempts                int      `json:"attempts"`
	ReusedNodes             int      `json:"reused_nodes"`
	FixtureAgentInvocations *int     `json:"fixture_agent_invocations"`
	ProviderCalls           *int     `json:"provider_calls"`
	InputTokens             *int64   `json:"input_tokens"`
	CostUSD                 *float64 `json:"cost_usd"`
	Provenance              []string `json:"reuse_sources,omitempty"`
}
type IncrementalComparison struct {
	Full        IncrementalArm `json:"full"`
	Incremental IncrementalArm `json:"incremental"`
	Equivalent  bool           `json:"equivalent"`
	FewerTasks  bool           `json:"fewer_tasks"`
}

func CompareIncremental(full, incremental IncrementalArm) (IncrementalComparison, error) {
	out := IncrementalComparison{Full: full, Incremental: incremental}
	if full.DatasetHash == "" || full.DatasetHash != incremental.DatasetHash || full.OutputHash == "" || incremental.OutputHash == "" || full.Tasks < 0 || incremental.Tasks < 0 || full.Attempts < 0 || incremental.Attempts < 0 || incremental.ReusedNodes < 0 || len(incremental.Provenance) != incremental.ReusedNodes {
		return out, fmt.Errorf("INCREMENTAL_EVIDENCE_INCOMPLETE")
	}
	out.Equivalent = full.OutputHash == incremental.OutputHash && full.BusinessVerdict == "PASS" && incremental.BusinessVerdict == "PASS"
	out.FewerTasks = incremental.Tasks < full.Tasks && incremental.Attempts < full.Attempts
	return out, nil
}
