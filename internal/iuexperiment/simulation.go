package iuexperiment

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/tommyxie2026-tech/computecloud/internal/graph"
)

type Scenario struct {
	ID                string `json:"id"`
	Fault             string `json:"injected_fault"`
	ExpectedDetection string `json:"expected_detection"`
	Passed            bool   `json:"check_passed"`
	Sample            Sample `json:"sample"`
}
type Simulation struct {
	Coverage       CoverageExperimentReport `json:"coverage"`
	Pilot          PilotSimulation          `json:"pilot"`
	Version        string                   `json:"version"`
	RealModelCalls bool                     `json:"real_model_calls"`
	Execution      string                   `json:"execution"`
	Passed         bool                     `json:"checks_passed"`
	Scenarios      []Scenario               `json:"scenarios"`
	Limitations    []string                 `json:"limitations"`
}

// simulationCall is an independent, sorted reference calculator. It does not
// call Expected, read a Case's expected result or contact a model provider.
func simulationCall(fault string) Call {
	return func(ctx context.Context, m graph.ContextManifest) (Reply, error) {
		if e := ctx.Err(); e != nil {
			return Reply{}, e
		}
		if fault == "timeout" {
			return Reply{}, context.DeadlineExceeded
		}
		if fault == "provider_unavailable" {
			return Reply{}, errors.New("simulated provider unavailable")
		}
		if fault == "malformed_json" {
			return Reply{Raw: json.RawMessage(`{"version":`)}, nil
		}
		var in struct {
			Instructions string
			Local        []struct{ Text string }
			Rules        []struct{ Text string }
			Inputs       []graph.InputValue
		}
		if e := json.Unmarshal([]byte(m.Text), &in); e != nil {
			return Reply{}, e
		}
		var value any
		if in.Instructions == extractInstruction {
			if len(in.Local) != 1 {
				return Reply{}, errors.New("missing local evidence")
			}
			var order Order
			if e := json.Unmarshal([]byte(in.Local[0].Text), &order); e != nil {
				return Reply{}, e
			}
			if fault == "collapse_customer" || fault == "corrupt_identity" {
				order.Customer = "lost-customer"
			}
			value = order
		} else {
			orders := []Order{}
			var rule Rule
			if len(in.Rules) != 1 {
				return Reply{}, errors.New("missing rule")
			}
			if e := json.Unmarshal([]byte(in.Rules[0].Text), &rule); e != nil {
				return Reply{}, e
			}
			if len(in.Inputs) > 0 {
				var grouped map[string]Order
				if len(in.Inputs) != 1 || in.Inputs[0].Port != "orders" {
					return Reply{}, errors.New("input binding")
				}
				if e := json.Unmarshal([]byte(in.Inputs[0].Value), &grouped); e != nil {
					return Reply{}, e
				}
				for _, o := range grouped {
					orders = append(orders, o)
				}
			} else {
				for _, local := range in.Local {
					var o Order
					if e := json.Unmarshal([]byte(local.Text), &o); e != nil {
						return Reply{}, e
					}
					orders = append(orders, o)
				}
			}
			// Sorting and adjacent grouping are deliberately separate from the oracle's
			// map aggregation; a bug in Expected cannot generate the model's answer.
			sort.Slice(orders, func(i, j int) bool { return orders[i].ID < orders[j].ID })
			unique := []Order{}
			for _, o := range orders {
				if fault != "count_duplicates" && len(unique) > 0 && unique[len(unique)-1].ID == o.ID {
					if unique[len(unique)-1] != o {
						return Reply{}, errors.New("conflicting identity")
					}
					continue
				}
				unique = append(unique, o)
			}
			sort.Slice(unique, func(i, j int) bool { return unique[i].Customer < unique[j].Customer })
			var total int64
			for i := 0; i < len(unique); {
				j := i
				var amount int64
				for j < len(unique) && unique[j].Customer == unique[i].Customer {
					amount += unique[j].Paid
					j++
				}
				if fault != "omit_rule" && amount >= rule.Threshold {
					amount -= rule.Discount
					if amount < 0 {
						amount = 0
					}
				}
				total += amount
				i = j
			}
			if fault == "wrong_total" {
				total++
			}
			value = total
		}
		if fault == "schema_mismatch" {
			value = true
		}
		return Reply{Raw: encode(graph.NodeOutput{Version: "iu-node-output-v1", Values: map[string]json.RawMessage{"result": encode(value)}, Records: []graph.MemoryRecord{}})}, nil
	}
}

func Simulate(ctx context.Context) Simulation {
	r := Simulation{Version: "iu-simulation-v1", Execution: "deterministic_contract_simulation", Passed: true, Scenarios: []Scenario{}, Limitations: []string{
		"No real model calls; passing means contracts detected the configured conditions.",
		"No evidence of model intelligence, provider token consumption, distributed speedup or cost savings.",
		"Native Server/Worker recovery is checked separately by required CI tests.",
	}}
	add := func(c Case, arm, fault, expected string) {
		sample := Run(ctx, c, arm, "deterministic-simulator-v1", simulationCall(fault))
		sample.Repetition = 1
		passed := false
		switch expected {
		case "correct":
			passed = sample.Error == "" && sample.Correct && (arm != "iu" || sample.ExtractionCorrect != nil && *sample.ExtractionCorrect)
		case "execution_rejected":
			passed = sample.Error != "" && !sample.Correct
		case "wrong_amount":
			passed = sample.Error == "" && !sample.Correct && sample.Actual != nil
		case "semantic_corruption":
			passed = sample.Error == "" && sample.ExtractionCorrect != nil && !*sample.ExtractionCorrect
		}
		r.Scenarios = append(r.Scenarios, Scenario{c.ID + "/" + arm + "/" + fault, fault, expected, passed, sample})
		r.Passed = r.Passed && passed
	}
	cases := Cases()
	for _, c := range cases {
		for _, arm := range []string{"monolithic", "iu"} {
			add(c, arm, "none", "correct")
		}
	}
	add(cases[2], "iu", "collapse_customer", "wrong_amount")
	add(cases[0], "iu", "corrupt_identity", "semantic_corruption")
	add(cases[0], "iu", "omit_rule", "wrong_amount")
	add(cases[0], "monolithic", "wrong_total", "wrong_amount")
	add(cases[0], "iu", "wrong_total", "wrong_amount")
	add(cases[3], "iu", "count_duplicates", "wrong_amount")
	add(cases[0], "iu", "schema_mismatch", "execution_rejected")
	add(cases[0], "monolithic", "malformed_json", "execution_rejected")
	add(cases[0], "iu", "timeout", "execution_rejected")
	add(cases[0], "monolithic", "provider_unavailable", "execution_rejected")
	r.Pilot = SimulatePilot(ctx)
	r.Coverage = SimulateCoverage(ctx)
	r.Passed = r.Passed && r.Pilot.Passed && r.Coverage.Passed
	return r
}
