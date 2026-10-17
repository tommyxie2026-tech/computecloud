package iuexperiment

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/graph"
)

func TestOraclePreservesCustomerAndDeduplicates(t *testing.T) {
	cases := Cases()
	want := []int64{11000, 10000, 12000, 11000}
	for i, c := range cases {
		got, e := Expected(c)
		if e != nil || got != want[i] {
			t.Fatalf("%s=%d %v", c.ID, got, e)
		}
	}
	c := cases[0]
	c.Orders = append(c.Orders, Order{"a", "c", 1})
	if _, e := Expected(c); e == nil {
		t.Fatal("conflicting evidence accepted")
	}
}

func fixture(ctx context.Context, m graph.ContextManifest) (Reply, error) {
	var in struct {
		Instructions string
		Local        []struct{ Text string }
		Rules        []struct{ Text string }
		Inputs       []graph.InputValue
		Memory       graph.MemoryView
	}
	if e := json.Unmarshal([]byte(m.Text), &in); e != nil {
		return Reply{}, e
	}
	var value any
	if in.Instructions == extractInstruction {
		var o Order
		if e := json.Unmarshal([]byte(in.Local[0].Text), &o); e != nil {
			return Reply{}, e
		}
		value = o
	} else {
		c := Case{}
		if e := json.Unmarshal([]byte(in.Rules[0].Text), &c.Rule); e != nil {
			return Reply{}, e
		}
		if len(in.Inputs) > 0 {
			var grouped map[string]Order
			if e := json.Unmarshal([]byte(in.Inputs[0].Value), &grouped); e != nil {
				return Reply{}, e
			}
			for _, o := range grouped {
				c.Orders = append(c.Orders, o)
			}
			if len(in.Memory.Records) == 0 {
				panic("memory not presented")
			}
		} else {
			for _, s := range in.Local {
				var o Order
				if e := json.Unmarshal([]byte(s.Text), &o); e != nil {
					return Reply{}, e
				}
				c.Orders = append(c.Orders, o)
			}
		}
		amount, e := Expected(c)
		if e != nil {
			return Reply{}, e
		}
		value = amount
	}
	return Reply{Raw: json.RawMessage(`{"version":"iu-node-output-v1","records":[],"values":{"result":` + string(encode(value)) + `}}`)}, nil
}

func TestPairedExperimentUsesSameEvidenceAndActualContracts(t *testing.T) {
	for _, c := range Cases() {
		for _, arm := range []string{"monolithic", "iu"} {
			s := Run(context.Background(), c, arm, "fixture", fixture)
			if !s.Correct || s.Error != "" {
				t.Fatalf("%s/%s %+v", c.ID, arm, s)
			}
			if s.InputTokens != nil {
				t.Fatal("invented token count")
			}
			if arm == "iu" && (s.ModelCalls != len(c.Orders)+1 || s.MemoryRecords != len(c.Orders)) {
				t.Fatalf("missing lineage %+v", s)
			}
			if arm == "monolithic" && s.ModelCalls != 1 {
				t.Fatalf("wrong control %+v", s)
			}
		}
	}
}

func TestWrongAmountAndSchemaFailureAreNotSuccess(t *testing.T) {
	bad := func(context.Context, graph.ContextManifest) (Reply, error) {
		return Reply{Raw: json.RawMessage(`{"version":"iu-node-output-v1","records":[],"values":{"result":12000}}`)}, nil
	}
	s := Run(context.Background(), Cases()[0], "monolithic", "fixture", bad)
	if s.Correct || s.Error != "" {
		t.Fatalf("wrong answer classified incorrectly %+v", s)
	}
	badType := func(context.Context, graph.ContextManifest) (Reply, error) {
		return Reply{Raw: json.RawMessage(`{"version":"iu-node-output-v1","records":[],"values":{"result":true}}`)}, nil
	}
	s = Run(context.Background(), Cases()[0], "monolithic", "fixture", badType)
	if s.Correct || s.Error == "" {
		t.Fatal("schema violation passed")
	}
}

func TestSummaryDoesNotCallStartupFailureBadIntelligence(t *testing.T) {
	summary := Summarize([]Sample{{Arm: "iu", Error: "MODEL_OR_CONTRACT_FAILED"}, {Arm: "monolithic", Correct: true}})
	if summary["iu"].ValidOutputs != 0 || summary["iu"].AccuracyValid != nil {
		t.Fatal("startup failure fabricated quality score")
	}
	if summary["monolithic"].AccuracyValid == nil || *summary["monolithic"].AccuracyValid != 1 {
		t.Fatal("missing measured score")
	}
}

func TestCorrectFinalTotalDoesNotHideCorruptedIntermediateIdentity(t *testing.T) {
	corrupt := func(ctx context.Context, m graph.ContextManifest) (Reply, error) {
		r, e := fixture(ctx, m)
		if e != nil {
			return r, e
		}
		var input struct{ Instructions string }
		json.Unmarshal([]byte(m.Text), &input)
		if input.Instructions == extractInstruction {
			var output graph.NodeOutput
			json.Unmarshal(r.Raw, &output)
			var order Order
			json.Unmarshal(output.Values["result"], &order)
			order.Customer = "corrupted-customer"
			output.Values["result"] = encode(order)
			r.Raw = encode(output)
		}
		return r, nil
	}
	s := Run(context.Background(), Cases()[0], "iu", "fixture", corrupt)
	if !s.Correct || s.ExtractionCorrect == nil || *s.ExtractionCorrect {
		t.Fatalf("final total hid semantic corruption %+v", s)
	}
}
