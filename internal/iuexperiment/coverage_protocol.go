package iuexperiment

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"math"
	"time"
)

//go:embed testdata/coverage-v1/orders.json
var coverageBase []byte

const coveragePrompt = "Return only iu-node-output-v1 JSON, with values.result and records=[]. For extract return the original order fields unchanged. For total return orders, total_cents, currency and unit, applying frozen rules once per customer. State no invented evidence."

var coverageArms = []string{"monolithic", "iu", "iu_no_memory", "iu_no_verifier"}

type CoverageCase struct {
	ID            string     `json:"id"`
	Scene         string     `json:"scene"`
	Variant       string     `json:"variant"`
	Spec          graph.Spec `json:"spec"`
	ExpectedCents int64      `json:"expected_cents"`
	ExpectedKnown bool       `json:"expected_known"`
}
type CoverageProtocol struct {
	Version               string         `json:"version"`
	Model                 string         `json:"model"`
	Runtime               string         `json:"runtime"`
	CLIVersion            string         `json:"cli_version"`
	Effort                string         `json:"effort"`
	AdapterVersion        string         `json:"adapter_version"`
	PromptHash            string         `json:"prompt_sha256"`
	MaxCalls              int            `json:"max_cli_attempts"`
	CallTimeoutSeconds    int            `json:"call_timeout_seconds"`
	OverallTimeoutSeconds int            `json:"overall_timeout_seconds"`
	Samples               []CoverageCase `json:"samples"`
	SHA256                string         `json:"sha256"`
}

func coverageCases() ([]CoverageCase, error) {
	out := []CoverageCase{}
	for scene := 0; scene < 6; scene++ {
		for variant := 0; variant < 3; variant++ {
			var spec graph.Spec
			if e := json.Unmarshal(coverageBase, &spec); e != nil {
				return nil, e
			}
			sources := map[string]graph.Source{}
			for _, s := range spec.Sources {
				sources[s.ID] = s
			}
			var goal map[string]any
			json.Unmarshal([]byte(sources["goal"].Text), &goal)
			expected := int64(11000)
			switch scene {
			case 1:
				one := sources["one"]
				one.Text = `{"order_id":"a","customer_id":"c","paid_cents":6000,"is_special":true}`
				sources["one"] = one
				sources["exception"] = graph.Source{ID: "exception", Text: `{"type":"special_no_discount"}`, Role: "user", MessageIndex: 4}
				goal["exception_source_ids"] = []string{"exception"}
				expected = 12000
			case 2:
				two := sources["two"]
				two.Text = `{"order_id":"b","customer_id":"other","paid_cents":6000,"is_special":false}`
				sources["two"] = two
				expected = 12000
			case 3:
				two := sources["two"]
				two.Text = sources["one"].Text
				sources["two"] = two
				expected = 6000
			case 4:
				rule := sources["rule"]
				rule.Text = `{"threshold_cents":10000,"discount_cents":2000,"max_discounts_per_customer":1}`
				sources["rule"] = rule
				expected = 10000
			}
			if variant == 2 {
				switch scene % 3 {
				case 0:
					goal["unit"] = "unresolved"
				case 1:
					sources["rule"] = graph.Source{ID: "rule", Text: `{"unknown_rule":true}`, Role: "user", MessageIndex: 3}
				case 2:
					sources["one"] = graph.Source{ID: "one", Text: "EVIDENCE_UNAVAILABLE", Role: "user", MessageIndex: 1}
				}
			}
			raw, _ := json.Marshal(goal)
			g := sources["goal"]
			g.Text = string(raw)
			sources["goal"] = g
			spec.Sources = nil
			spans := []graph.Span{}
			for _, id := range []string{"goal", "one", "two", "rule", "exception"} {
				s, ok := sources[id]
				if !ok {
					continue
				}
				s.SHA256 = graph.Hash([]byte(s.Text))
				spec.Sources = append(spec.Sources, s)
				spans = append(spans, graph.Span{Source: id, End: len(s.Text), SHA256: s.SHA256})
			}
			spec.Nodes[0].Spans = spans
			spec.Coverage.GoalSpecHash = graph.Hash(raw)
			for i := range spec.Coverage.Obligations {
				spec.Coverage.Obligations[i].Sources = spans
			}
			spec.Coverage.SHA256 = spec.Coverage.Digest()
			p, e := graph.Compile(spec)
			if e != nil {
				return nil, e
			}
			names := []string{"cross_threshold", "late_exception", "customer_reference", "duplicate_conflict", "rule_revision", "evidence_availability"}
			out = append(out, CoverageCase{ID: fmt.Sprintf("case-%02d", len(out)+1), Scene: names[scene], Variant: []string{"correct", "violation", "unknown"}[variant], Spec: p.Spec, ExpectedCents: expected, ExpectedKnown: variant != 2})
		}
	}
	return out, nil
}
func (p CoverageProtocol) digest() string { p.SHA256 = ""; return graph.Hash(encode(p)) }
func NewCoverageProtocol(model, runtime, cliVersion, effort string) (CoverageProtocol, error) {
	cases, e := coverageCases()
	p := CoverageProtocol{Version: "iu-coverage-protocol-v1", Model: model, Runtime: runtime, CLIVersion: cliVersion, Effort: effort, AdapterVersion: "iu-coverage-cli-v1", PromptHash: graph.Hash([]byte(coveragePrompt)), Samples: cases, CallTimeoutSeconds: 60, OverallTimeoutSeconds: 1200}
	for _, c := range cases {
		p.MaxCalls += 4 * 3 * (len(coverageOrderSources(c.Spec)) + 1)
	}
	p.SHA256 = p.digest()
	if e != nil {
		return p, e
	}
	return p, p.Validate()
}
func (p CoverageProtocol) Validate() error {
	cases, e := coverageCases()
	if e != nil {
		return e
	}
	expected, e := NewCoverageProtocolUnchecked(p.Model, p.Runtime, p.CLIVersion, p.Effort, cases)
	if e != nil {
		return e
	}
	if p.SHA256 != p.digest() || string(encode(p)) != string(encode(expected)) {
		return fmt.Errorf("COVERAGE_PROTOCOL_DRIFT")
	}
	return nil
}
func NewCoverageProtocolUnchecked(model, runtime, cliVersion, effort string, cases []CoverageCase) (CoverageProtocol, error) {
	if model == "" || len(model) > 256 || cliVersion == "" || len(cliVersion) > 512 || (runtime != "codex_exec" && runtime != "claude_print") || (effort != "low" && effort != "medium" && effort != "high") {
		return CoverageProtocol{}, fmt.Errorf("COVERAGE_PROTOCOL_BINDING")
	}
	p := CoverageProtocol{Version: "iu-coverage-protocol-v1", Model: model, Runtime: runtime, CLIVersion: cliVersion, Effort: effort, AdapterVersion: "iu-coverage-cli-v1", PromptHash: graph.Hash([]byte(coveragePrompt)), Samples: cases, CallTimeoutSeconds: 60, OverallTimeoutSeconds: 1200}
	for _, c := range cases {
		p.MaxCalls += 4 * 3 * (len(coverageOrderSources(c.Spec)) + 1)
	}
	p.SHA256 = p.digest()
	return p, nil
}

type CoverageInvocation struct {
	Stage        string          `json:"stage"`
	Round        int             `json:"round"`
	ContextHash  string          `json:"context_sha256"`
	ReadViewHash string          `json:"read_view_sha256"`
	Result       json.RawMessage `json:"raw_output,omitempty"`
	Error        string          `json:"error_code,omitempty"`
	UsageOrigin  string          `json:"usage_origin"`
	CostOrigin   string          `json:"cost_origin"`
}
type CoverageAttempt struct {
	Round           int             `json:"round"`
	Result          json.RawMessage `json:"result,omitempty"`
	Verdict         string          `json:"verdict"`
	Error           string          `json:"error_code,omitempty"`
	BusinessPass    *bool           `json:"independent_business_pass"`
	FieldsPreserved *bool           `json:"fields_preserved"`
}
type CoverageSample struct {
	Attempts               []CoverageAttempt    `json:"attempts"`
	Invocations            []CoverageInvocation `json:"invocations"`
	InitialBusinessPass    *bool                `json:"initial_business_pass"`
	InitialFieldsPreserved *bool                `json:"initial_fields_preserved"`
	CaseID                 string               `json:"case_id"`
	Arm                    string               `json:"arm"`
	Calls                  int                  `json:"node_invocations"`
	Rounds                 int                  `json:"rounds"`
	Error                  string               `json:"error_code,omitempty"`
	PipelineAcceptance     bool                 `json:"pipeline_acceptance"`
	BusinessPass           *bool                `json:"independent_business_pass"`
	FieldsPreserved        *bool                `json:"fields_preserved"`
	FalseAcceptance        bool                 `json:"false_acceptance"`
	VerifierCalls          int                  `json:"verifier_calls"`
	VerifierMicros         int64                `json:"verifier_us"`
	ReadBytes              int                  `json:"context_bytes"`
	WallMS                 int64                `json:"wall_ms"`
	Verdict                string               `json:"verdict"`
	InitialVerdict         string               `json:"initial_verdict"`
	InputTokens            *int64               `json:"input_tokens"`
	OutputTokens           *int64               `json:"output_tokens"`
	CostUSD                *float64             `json:"reported_cost_usd"`
	Result                 json.RawMessage      `json:"result,omitempty"`
}
type CoverageExperimentReport struct {
	Metrics            map[string]DetailedMetric `json:"metrics"`
	Version            string                    `json:"version"`
	Execution          string                    `json:"execution"`
	Protocol           CoverageProtocol          `json:"protocol"`
	Samples            []CoverageSample          `json:"samples"`
	Calls              int                       `json:"calls"`
	ProviderCalls      *int                      `json:"provider_calls"`
	FixtureInvocations int                       `json:"fixture_invocations"`
	Passed             bool                      `json:"checks_passed"`
	ProviderStatus     string                    `json:"provider_status"`
}
type coverageCallParameters struct {
	CaseID string `json:"case_id"`
	Stage  string `json:"stage"`
	Round  int    `json:"round"`
}

func coverageOrderSources(s graph.Spec) []string {
	for _, src := range s.Sources {
		if src.ID == s.Coverage.GoalSourceID {
			var g struct {
				Orders []string `json:"order_source_ids"`
			}
			json.Unmarshal([]byte(src.Text), &g)
			return g.Orders
		}
	}
	return nil
}
func coverageStage(ctx context.Context, p CoverageProtocol, c CoverageCase, arm string, round int, call Call, stats *CoverageSample) (json.RawMessage, error) {
	view := graph.EmptyView()
	inputs := []graph.InputValue{}
	rules := []graph.Span{}
	orderIDs := map[string]bool{}
	for _, id := range coverageOrderSources(c.Spec) {
		orderIDs[id] = true
	}
	for _, s := range c.Spec.Sources {
		if !orderIDs[s.ID] {
			rules = append(rules, graph.Span{Source: s.ID, End: len(s.Text), SHA256: s.SHA256})
		}
	}
	lastContextHash := ""
	invoke := func(stage string, local []graph.Span, v graph.MemoryView, ins []graph.InputValue) (json.RawMessage, error) {
		m, e := graph.CompileContext(graph.ContextRequest{Sources: c.Spec.Sources, Rules: rules, Spans: local, Instructions: coveragePrompt, View: v, Inputs: ins, Model: p.Model, Parameters: encode(coverageCallParameters{c.ID, stage, round})})
		if e != nil {
			return nil, e
		}
		lastContextHash = m.SHA256
		stats.ReadBytes += len(m.Text)
		stats.Calls++
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(p.CallTimeoutSeconds)*time.Second)
		reply, e := call(callCtx, m)
		cancel()
		invocation := CoverageInvocation{Stage: stage, Round: round, ContextHash: m.SHA256, ReadViewHash: m.ReadViewHash, Result: reply.Raw, UsageOrigin: reply.UsageOrigin, CostOrigin: reply.CostOrigin}
		if e != nil {
			invocation.Error = "CALL_FAILED"
		}
		stats.Invocations = append(stats.Invocations, invocation)
		if e != nil {
			return nil, e
		}
		if reply.InputTokens == nil {
			stats.InputTokens = nil
		} else if stats.InputTokens != nil {
			*stats.InputTokens += *reply.InputTokens
		}
		if reply.OutputTokens == nil {
			stats.OutputTokens = nil
		} else if stats.OutputTokens != nil {
			*stats.OutputTokens += *reply.OutputTokens
		}
		if reply.CostUSD == nil || math.IsNaN(*reply.CostUSD) || math.IsInf(*reply.CostUSD, 0) || *reply.CostUSD < 0 {
			stats.CostUSD = nil
		} else if stats.CostUSD != nil {
			*stats.CostUSD += *reply.CostUSD
		}
		out, e := graph.BuildOutput(reply.Raw, graph.Invocation{Version: "computecloud-job-io-v2", GoalID: "local-coverage-harness", Generation: int64(round + 1), NodeID: stage, Context: m, View: v, Outputs: []graph.Port{{Name: "result", Schema: "object-v1"}}, Schemas: map[string]json.RawMessage{"object-v1": json.RawMessage(`{"type":"object"}`)}})
		if e != nil {
			return nil, e
		}
		return out.Values["result"], nil
	}
	if arm != "monolithic" {
		views := []graph.MemoryView{}
		for i, id := range coverageOrderSources(c.Spec) {
			var spans []graph.Span
			for _, src := range c.Spec.Sources {
				if src.ID == id {
					spans = []graph.Span{{Source: src.ID, End: len(src.Text), SHA256: src.SHA256}}
				}
			}
			raw, e := invoke("extract", spans, graph.EmptyView(), nil)
			if e != nil {
				return nil, e
			}
			inputs = append(inputs, graph.InputValue{Node: fmt.Sprintf("local_%d", i), Port: "order", SHA256: graph.Hash(raw), Value: string(raw)})
			record := graph.MemoryRecord{ID: fmt.Sprintf("observed_%d", i), Kind: "observation", Payload: raw, Validation: "unchecked", Validity: "VALID"}
			d := graph.Delta{Version: "iu-memory-delta-v1", ReadViewHash: graph.EmptyView().SHA256, ContextHash: lastContextHash, Records: []graph.MemoryRecord{record}}
			d, e = graph.BindDeltaSources(d, append(append([]graph.Span{}, rules...), spans...))
			if e != nil {
				return nil, e
			}
			v, e := graph.ApplyDelta(graph.EmptyView(), d)
			if e != nil {
				return nil, e
			}
			views = append(views, v)
		}
		var e error
		view, e = graph.MergeViews(views)
		if e != nil {
			return nil, e
		}
		if arm == "iu_no_memory" {
			view = graph.EmptyView()
		}
	}
	local := []graph.Span{}
	if arm == "monolithic" {
		for _, src := range c.Spec.Sources {
			if orderIDs[src.ID] {
				local = append(local, graph.Span{Source: src.ID, End: len(src.Text), SHA256: src.SHA256})
			}
		}
	}
	return invoke("total", local, view, inputs)
}
func coverageOracle(c CoverageCase, raw json.RawMessage) (*bool, *bool) {
	if !c.ExpectedKnown {
		return nil, nil
	}
	var result graph.CoverageOrderResult
	fields := json.Unmarshal(raw, &result) == nil && result.Orders != nil && result.Total != nil
	expected := map[string]string{}
	for _, id := range coverageOrderSources(c.Spec) {
		for _, src := range c.Spec.Sources {
			if src.ID == id {
				var o graph.CoverageOrder
				if json.Unmarshal([]byte(src.Text), &o) != nil {
					fields = false
				} else {
					expected[o.ID] = string(encode(o))
				}
			}
		}
	}
	actual := map[string]string{}
	for _, o := range result.Orders {
		if expected[o.ID] != string(encode(o)) {
			fields = false
		}
		if prior, ok := actual[o.ID]; ok && prior != string(encode(o)) {
			fields = false
		}
		actual[o.ID] = string(encode(o))
	}
	if len(actual) != len(expected) {
		fields = false
	}
	for id, value := range expected {
		if actual[id] != value {
			fields = false
		}
	}
	pass := fields && result.Total != nil && *result.Total == c.ExpectedCents && result.Currency == "CNY" && result.Unit == "cent"
	return &pass, &fields
}
func RunCoverageProtocol(ctx context.Context, p CoverageProtocol, call Call) (CoverageExperimentReport, error) {
	r := CoverageExperimentReport{Version: "iu-coverage-experiment-v1", Execution: "local_cli_no_server_worker_or_task", Protocol: p, Samples: []CoverageSample{}, ProviderStatus: "NOT_RUN"}
	if e := p.Validate(); e != nil {
		return r, e
	}
	if call == nil {
		return r, fmt.Errorf("COVERAGE_CALL_REQUIRED")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.OverallTimeoutSeconds)*time.Second)
	defer cancel()
	for index, c := range p.Samples {
		for a := 0; a < 4; a++ {
			arm := coverageArms[(a+index)%4]
			s := CoverageSample{CaseID: c.ID, Arm: arm, Verdict: "UNKNOWN"}
			zero := int64(0)
			zeroOut := int64(0)
			zeroCost := float64(0)
			s.InputTokens = &zero
			s.OutputTokens = &zeroOut
			s.CostUSD = &zeroCost
			started := time.Now()
			if ctx.Err() != nil {
				s.Error = "NOT_ATTEMPTED"
				s.InputTokens = nil
				s.OutputTokens = nil
				s.CostUSD = nil
			} else {
				for round := 0; round < 3; round++ {
					s.Rounds = round + 1
					bounded := func(ctx context.Context, m graph.ContextManifest) (Reply, error) {
						if r.Calls >= p.MaxCalls {
							return Reply{}, fmt.Errorf("COVERAGE_CALL_BUDGET")
						}
						r.Calls++
						return call(ctx, m)
					}
					raw, e := coverageStage(ctx, p, c, arm, round, bounded, &s)
					if e != nil {
						s.Error = "EXECUTION_OR_CONTRACT_FAILED"
						s.Attempts = append(s.Attempts, CoverageAttempt{Round: round, Verdict: "UNKNOWN", Error: s.Error})
						s.InputTokens = nil
						s.OutputTokens = nil
						s.CostUSD = nil
						break
					}
					s.Result = raw
					s.BusinessPass, s.FieldsPreserved = coverageOracle(c, raw)
					if round == 0 {
						s.InitialBusinessPass = s.BusinessPass
						s.InitialFieldsPreserved = s.FieldsPreserved
					}
					if arm == "iu_no_verifier" {
						s.Verdict = "UNCHECKED"
						s.PipelineAcceptance = true
						s.Attempts = append(s.Attempts, CoverageAttempt{Round: round, Result: raw, Verdict: s.Verdict, BusinessPass: s.BusinessPass, FieldsPreserved: s.FieldsPreserved})
						break
					}
					s.VerifierCalls++
					verifyStart := time.Now()
					proof := graph.RunCoverageVerifier(ctx, graph.CoverageVerifierProfile{Version: "1", Kind: "orders-v1", Scope: "orders", TimeoutMS: 1000}, c.Spec, map[string]json.RawMessage{"extract/result": raw})
					s.VerifierMicros += time.Since(verifyStart).Microseconds()
					s.Verdict = proof.Verdict
					if round == 0 {
						s.InitialVerdict = s.Verdict
					}
					s.PipelineAcceptance = s.Verdict == "PASS"
					s.Attempts = append(s.Attempts, CoverageAttempt{Round: round, Result: raw, Verdict: s.Verdict, BusinessPass: s.BusinessPass, FieldsPreserved: s.FieldsPreserved})
					if s.Verdict != "FAIL" {
						break
					}
				}
			}
			s.FalseAcceptance = s.PipelineAcceptance && s.BusinessPass != nil && !*s.BusinessPass
			s.WallMS = time.Since(started).Milliseconds()
			r.Samples = append(r.Samples, s)
		}
	}
	metricSamples := []Sample{}
	for _, sample := range r.Samples {
		var actual graph.CoverageOrderResult
		json.Unmarshal(sample.Result, &actual)
		failureClass := ""
		if sample.Error == "NOT_ATTEMPTED" {
			failureClass = "not_attempted"
		}
		metricSamples = append(metricSamples, Sample{Arm: sample.Arm, CaseID: sample.CaseID, Actual: actual.Total, Error: sample.Error, FailureClass: failureClass, BusinessPass: sample.BusinessPass, ExtractionCorrect: sample.FieldsPreserved, FalseAcceptance: sample.FalseAcceptance, ModelCalls: sample.Calls, VerifierCalls: sample.VerifierCalls, VerifierMicros: sample.VerifierMicros, WallMS: sample.WallMS, InputTokens: sample.InputTokens, OutputTokens: sample.OutputTokens, CostUSD: sample.CostUSD})
	}
	r.Metrics = DetailedMetrics(metricSamples)
	if r.Calls > 0 {
		r.ProviderStatus = "CALL_ADAPTER_INVOKED_PROVIDER_UNVERIFIED"
	}
	return r, nil
}
