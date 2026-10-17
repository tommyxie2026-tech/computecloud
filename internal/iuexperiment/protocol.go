package iuexperiment

import (
	"context"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"math"
	"sort"
)

const PilotAdapterVersion = "iu-local-cli-v2"

type PilotSample struct {
	Case       Case `json:"case"`
	Repetition int  `json:"repetition"`
}
type PilotProtocol struct {
	OverallTimeoutSeconds       int           `json:"overall_timeout_seconds"`
	IncrementalControl          string        `json:"incremental_control"`
	Version                     string        `json:"version"`
	Model                       string        `json:"requested_model"`
	Runtime                     string        `json:"runtime"`
	CLIVersion                  string        `json:"cli_version"`
	AdapterVersion              string        `json:"adapter_version"`
	Effort                      string        `json:"effort"`
	PromptHash                  string        `json:"prompt_sha256"`
	Arms                        []string      `json:"arms"`
	Samples                     []PilotSample `json:"samples"`
	MaxCalls                    int           `json:"max_cli_attempts"`
	CallTimeoutSeconds          int           `json:"call_timeout_seconds"`
	WorkflowPilot               bool          `json:"workflow_pilot"`
	NonInferiorityMargin        float64       `json:"exploratory_noninferiority_margin"`
	CostRatioTarget             float64       `json:"exploratory_cost_ratio_target"`
	FormalPowerAnalysisComplete bool          `json:"formal_power_analysis_complete"`
	FeeHardLimit                *float64      `json:"fee_hard_limit_usd"`
	SHA256                      string        `json:"sha256"`
}

func PromptHash() string {
	return graph.Hash(encode(struct {
		Extract, Total string
		Schemas        any
	}{extractInstruction, totalInstruction, schemas}))
}
func NewPilotProtocol(model, runtime, cliVersion, effort string) (PilotProtocol, error) {
	p := PilotProtocol{Version: "iu-pilot-protocol-v1", OverallTimeoutSeconds: 1200, IncrementalControl: "native_task_evidence_separate", Model: model, Runtime: runtime, CLIVersion: cliVersion, AdapterVersion: PilotAdapterVersion, Effort: effort, PromptHash: PromptHash(), Arms: []string{"monolithic", "iu", "iu_no_memory", "iu_no_verifier"}, CallTimeoutSeconds: 60, WorkflowPilot: true, NonInferiorityMargin: 0.05, CostRatioTarget: 0.8}
	for repeat := 1; repeat <= 5; repeat++ {
		for _, c := range Cases() {
			p.Samples = append(p.Samples, PilotSample{c, repeat})
			p.MaxCalls += 1 + 3*(len(c.Orders)+1)
		}
	}
	p.SHA256 = p.digest()
	return p, p.Validate()
}
func (p PilotProtocol) digest() string { p.SHA256 = ""; return graph.Hash(encode(p)) }
func (p PilotProtocol) Validate() error {
	if p.Version != "iu-pilot-protocol-v1" || p.Model == "" || len(p.Model) > 256 || p.CLIVersion == "" || len(p.CLIVersion) > 512 || (p.Runtime != "codex_exec" && p.Runtime != "claude_print") || p.AdapterVersion != PilotAdapterVersion || p.PromptHash != PromptHash() || p.SHA256 != p.digest() || p.OverallTimeoutSeconds < 1 || p.OverallTimeoutSeconds > 7200 || p.IncrementalControl != "native_task_evidence_separate" || p.CallTimeoutSeconds < 1 || p.CallTimeoutSeconds > 300 || !p.WorkflowPilot || p.FormalPowerAnalysisComplete || p.FeeHardLimit != nil || p.NonInferiorityMargin <= 0 || p.NonInferiorityMargin >= 1 || p.CostRatioTarget <= 0 || p.CostRatioTarget > 1 || (p.Effort != "low" && p.Effort != "medium" && p.Effort != "high") {
		return fmt.Errorf("PILOT_PROTOCOL_INVALID_OR_UNSUPPORTED")
	}
	if len(p.Samples) != 20 || len(p.Arms) != 4 {
		return fmt.Errorf("PILOT_PROTOCOL_COVERAGE")
	}
	arms := map[string]bool{}
	for _, a := range p.Arms {
		if a != "monolithic" && a != "iu" && a != "iu_no_memory" && a != "iu_no_verifier" || arms[a] {
			return fmt.Errorf("PILOT_PROTOCOL_ARMS")
		}
		arms[a] = true
	}
	required := 0
	for i, s := range p.Samples {
		cases := Cases()
		if s.Repetition != i/4+1 || graph.Hash(encode(s.Case)) != graph.Hash(encode(cases[i%4])) {
			return fmt.Errorf("PILOT_PROTOCOL_SAMPLE_DRIFT")
		}
		required += 1 + 3*(len(s.Case.Orders)+1)
	}
	if p.MaxCalls != required {
		return fmt.Errorf("PILOT_PROTOCOL_CALL_BUDGET")
	}
	return nil
}

type DetailedMetric struct {
	NotAttempted        int      `json:"not_attempted"`
	LatencyMeasured     int      `json:"latency_denominator"`
	Total               int      `json:"total_samples"`
	ValidOutputs        int      `json:"valid_outputs"`
	ExecutionFailures   int      `json:"execution_failures"`
	BusinessPasses      int      `json:"independent_business_passes"`
	FalseAccepts        int      `json:"false_accepts"`
	CompletionRate      float64  `json:"completion_rate_all_samples"`
	BusinessPassRate    float64  `json:"business_pass_rate_all_samples"`
	FieldPreservation   *float64 `json:"field_preservation_measured"`
	FieldMeasured       int      `json:"field_preservation_denominator"`
	Calls               int      `json:"cli_attempts"`
	VerifierCalls       int      `json:"verifier_calls"`
	VerifierMicros      int64    `json:"verifier_us"`
	P50MS               *int64   `json:"p50_ms_nearest_rank"`
	P95MS               *int64   `json:"p95_ms_nearest_rank"`
	InputTokens         *int64   `json:"input_tokens"`
	OutputTokens        *int64   `json:"output_tokens"`
	CostUSD             *float64 `json:"cost_usd"`
	CostPerBusinessPass *float64 `json:"cost_per_business_pass_usd"`
}

func DetailedMetrics(samples []Sample) map[string]DetailedMetric {
	groups := map[string][]Sample{}
	for _, s := range samples {
		groups[s.Arm] = append(groups[s.Arm], s)
	}
	out := map[string]DetailedMetric{}
	for arm, ss := range groups {
		m := DetailedMetric{Total: len(ss)}
		latencies := []int64{}
		fieldPass := 0
		var in, outTokens int64
		var cost float64
		allIn, allOut, allCost := true, true, true
		for _, s := range ss {
			m.Calls += s.ModelCalls
			m.VerifierCalls += s.VerifierCalls
			m.VerifierMicros += s.VerifierMicros
			if s.FailureClass != "not_attempted" {
				latencies = append(latencies, s.WallMS)
				m.LatencyMeasured++
			} else {
				m.NotAttempted++
			}
			if s.Error == "" && s.Actual != nil {
				m.ValidOutputs++
			} else {
				m.ExecutionFailures++
			}
			if s.BusinessPass != nil && *s.BusinessPass {
				m.BusinessPasses++
			}
			if s.FalseAcceptance {
				m.FalseAccepts++
			}
			if s.ExtractionCorrect != nil {
				m.FieldMeasured++
				if *s.ExtractionCorrect {
					fieldPass++
				}
			}
			if s.InputTokens == nil {
				allIn = false
			} else {
				in += *s.InputTokens
			}
			if s.OutputTokens == nil {
				allOut = false
			} else {
				outTokens += *s.OutputTokens
			}
			if s.CostUSD == nil || math.IsNaN(*s.CostUSD) || math.IsInf(*s.CostUSD, 0) || *s.CostUSD < 0 {
				allCost = false
			} else {
				cost += *s.CostUSD
			}
		}
		m.CompletionRate = float64(m.ValidOutputs) / float64(m.Total)
		m.BusinessPassRate = float64(m.BusinessPasses) / float64(m.Total)
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		if len(latencies) > 0 {
			median := latencies[int(math.Ceil(float64(len(latencies))*.5))-1]
			m.P50MS = &median
			p95 := latencies[int(math.Ceil(float64(len(latencies))*.95))-1]
			m.P95MS = &p95
		}

		if m.FieldMeasured > 0 {
			v := float64(fieldPass) / float64(m.FieldMeasured)
			m.FieldPreservation = &v
		}
		if allIn {
			m.InputTokens = &in
		}
		if allOut {
			m.OutputTokens = &outTokens
		}
		if allCost {
			m.CostUSD = &cost
			if m.BusinessPasses > 0 {
				v := cost / float64(m.BusinessPasses)
				m.CostPerBusinessPass = &v
			}
		}
		out[arm] = m
	}
	return out
}

type PilotSimulation struct {
	Version            string                    `json:"version"`
	Protocol           PilotProtocol             `json:"protocol"`
	Samples            []Sample                  `json:"samples"`
	Metrics            map[string]DetailedMetric `json:"metrics"`
	FixtureInvocations int                       `json:"fixture_invocations"`
	ProviderCalls      int                       `json:"provider_calls"`
	Passed             bool                      `json:"checks_passed"`
}

func SimulatePilot(ctx context.Context) PilotSimulation {
	p, e := NewPilotProtocol("deterministic-fixture-model", "codex_exec", "simulation-fixture-1", "low")
	out := PilotSimulation{Version: "iu-pilot-simulation-v1", Protocol: p, Passed: e == nil, Samples: []Sample{}}
	for i, entry := range p.Samples {
		arms := append([]string{}, p.Arms...)
		rotation := i % len(arms)
		arms = append(arms[rotation:], arms[:rotation]...)
		for _, arm := range arms {
			sample := Run(ctx, entry.Case, arm, p.Model, simulationCall("none"))
			sample.Repetition = entry.Repetition
			out.Samples = append(out.Samples, sample)
			out.FixtureInvocations += sample.ModelCalls
			out.Passed = out.Passed && sample.Error == "" && sample.BusinessPass != nil && *sample.BusinessPass && sample.InputTokens == nil && sample.CostUSD == nil
		}
	}
	out.Passed = out.Passed && out.FixtureInvocations == p.MaxCalls && len(out.Samples) == 80
	out.Metrics = DetailedMetrics(out.Samples)
	return out
}
