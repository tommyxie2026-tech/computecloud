package iuexperiment

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
)

func coverageFixtureCall(p CoverageProtocol) Call {
	return func(ctx context.Context, m graph.ContextManifest) (Reply, error) {
		var body struct {
			Local []struct {
				Text string `json:"text"`
			} `json:"local"`
			Rules []struct {
				Text   string     `json:"text"`
				Source graph.Span `json:"source"`
			} `json:"rules"`
			Inputs     []graph.InputValue     `json:"inputs"`
			Parameters coverageCallParameters `json:"parameters"`
		}
		if e := json.Unmarshal([]byte(m.Text), &body); e != nil {
			return Reply{}, e
		}
		var c CoverageCase
		for _, s := range p.Samples {
			if s.ID == body.Parameters.CaseID {
				c = s
				break
			}
		}
		orders := []graph.CoverageOrder{}
		if body.Parameters.Stage == "extract" {
			if len(body.Local) != 1 {
				return Reply{}, fmt.Errorf("fixture local")
			}
			var o graph.CoverageOrder
			if e := json.Unmarshal([]byte(body.Local[0].Text), &o); e != nil {
				return Reply{}, e
			}
			if c.Variant == "violation" && body.Parameters.Round == 0 {
				o.ID = "corrupt-" + o.ID
			}
			return Reply{Raw: encode(graph.NodeOutput{Version: "iu-node-output-v1", Values: map[string]json.RawMessage{"result": encode(o)}, Records: []graph.MemoryRecord{}})}, nil
		}
		if len(body.Inputs) > 0 {
			for _, in := range body.Inputs {
				var o graph.CoverageOrder
				if e := json.Unmarshal([]byte(in.Value), &o); e != nil {
					return Reply{}, e
				}
				orders = append(orders, o)
			}
		} else {
			for _, local := range body.Local {
				var o graph.CoverageOrder
				if e := json.Unmarshal([]byte(local.Text), &o); e != nil {
					return Reply{}, e
				}
				orders = append(orders, o)
			}
			if c.Variant == "violation" && body.Parameters.Round == 0 && len(orders) > 0 {
				orders[0].ID = "corrupt-" + orders[0].ID
			}
		}
		threshold, discount := int64(10000), int64(1000)
		currency, unit := "CNY", "cent"
		exclude := false
		for _, r := range body.Rules {
			if r.Source.Source == "goal" {
				var g struct {
					Currency string `json:"currency"`
					Unit     string `json:"unit"`
				}
				json.Unmarshal([]byte(r.Text), &g)
				currency, unit = g.Currency, g.Unit
			}
			if r.Source.Source == "rule" {
				var rule struct {
					Threshold *int64 `json:"threshold_cents"`
					Discount  *int64 `json:"discount_cents"`
				}
				if json.Unmarshal([]byte(r.Text), &rule) != nil || rule.Threshold == nil || rule.Discount == nil {
					return Reply{}, fmt.Errorf("unsupported fixture rule")
				}
				threshold, discount = *rule.Threshold, *rule.Discount
			}
			if r.Source.Source == "exception" {
				exclude = true
			}
		}
		var total int64
		seen := map[string]bool{}
		groups := map[string]int64{}
		for _, o := range orders {
			if o.Paid == nil || o.Special == nil {
				return Reply{}, fmt.Errorf("fixture incomplete order")
			}
			if seen[o.ID] {
				continue
			}
			seen[o.ID] = true
			total += *o.Paid
			if !exclude || !*o.Special {
				groups[o.Customer] += *o.Paid
			}
		}
		for _, sum := range groups {
			if sum >= threshold {
				d := discount
				if d > sum {
					d = sum
				}
				total -= d
			}
		}
		result := graph.CoverageOrderResult{Orders: orders, Total: &total, Currency: currency, Unit: unit}
		return Reply{Raw: encode(graph.NodeOutput{Version: "iu-node-output-v1", Values: map[string]json.RawMessage{"result": encode(result)}, Records: []graph.MemoryRecord{}})}, nil
	}
}
func SimulateCoverage(ctx context.Context) CoverageExperimentReport {
	p, e := NewCoverageProtocol("fixture", "codex_exec", "fixture-1", "low")
	if e != nil {
		return CoverageExperimentReport{}
	}
	r, e := RunCoverageProtocol(ctx, p, coverageFixtureCall(p))
	zero := 0
	r.ProviderCalls = &zero
	r.FixtureInvocations = r.Calls
	r.ProviderStatus = "NOT_RUN"
	r.Passed = e == nil && len(r.Samples) == 72
	knownFailures, unknowns, repairs := 0, 0, 0
	for _, s := range r.Samples {
		if s.FalseAcceptance {
			knownFailures++
		}
		if s.Verdict == "UNKNOWN" || s.Error != "" {
			unknowns++
		}
		if s.Rounds > 1 && s.PipelineAcceptance {
			repairs++
		}
		if s.InputTokens != nil || s.CostUSD != nil {
			r.Passed = false
		}
	}
	r.Passed = r.Passed && knownFailures > 0 && unknowns > 0 && repairs > 0
	return r
}
