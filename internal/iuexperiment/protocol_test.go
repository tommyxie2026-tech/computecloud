package iuexperiment

import (
	"context"
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"testing"
)

func TestPilotProtocolFreezesTwentyCasesAndBudgets(t *testing.T) {
	p, e := NewPilotProtocol("fixture-model", "codex_exec", "fixture-cli-1", "low")
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Samples) != 20 || len(p.Arms) != 4 || p.MaxCalls != 215 || !p.WorkflowPilot {
		t.Fatalf("pilot %+v", p)
	}
	if e = p.Validate(); e != nil {
		t.Fatal(e)
	}
	p.Model = "changed"
	if e = p.Validate(); e == nil {
		t.Fatal("mutated frozen protocol accepted")
	}
}
func TestAblationsKeepMemoryAndVerificationEffectsSeparate(t *testing.T) {
	for _, arm := range []string{"monolithic", "iu", "iu_no_memory", "iu_no_verifier"} {
		s := Run(context.Background(), Cases()[0], arm, "fixture", simulationCall("none"))
		if s.Error != "" || s.BusinessPass == nil || !*s.BusinessPass || s.PipelineAccepted == nil || !*s.PipelineAccepted {
			t.Fatalf("arm %s %+v", arm, s)
		}
		if arm == "iu_no_memory" && s.MemoryRecords != 0 {
			t.Fatal("memory ablation still supplied records")
		}
		if arm == "iu_no_verifier" && s.VerifierCalls != 0 {
			t.Fatal("verification ablation still executed verifier")
		}
	}
	corrupt := func(ctx context.Context, m graph.ContextManifest) (Reply, error) {
		r, e := fixture(ctx, m)
		if e != nil {
			return r, e
		}
		var out graph.NodeOutput
		json.Unmarshal(r.Raw, &out)
		var body struct{ Instructions string }
		json.Unmarshal([]byte(m.Text), &body)
		if body.Instructions == extractInstruction {
			var o Order
			json.Unmarshal(out.Values["result"], &o)
			o.Customer = "wrong"
			out.Values["result"] = encode(o)
			r.Raw = encode(out)
		}
		return r, nil
	}
	verified := Run(context.Background(), Cases()[0], "iu", "fixture", corrupt)
	unchecked := Run(context.Background(), Cases()[0], "iu_no_verifier", "fixture", corrupt)
	if verified.PipelineAccepted == nil || *verified.PipelineAccepted || unchecked.PipelineAccepted == nil || !*unchecked.PipelineAccepted || !unchecked.FalseAcceptance {
		t.Fatal("false acceptance hidden")
	}
}
func TestDetailedMetricsUseAllSamplesAndUnknownCosts(t *testing.T) {
	actual := int64(11000)
	yes := true
	samples := []Sample{{Arm: "iu", Actual: &actual, Correct: true, BusinessPass: &yes, WallMS: 10}, {Arm: "iu", Error: "CLI_STARTUP_FAILED", WallMS: 100}}
	m := DetailedMetrics(samples)["iu"]
	if m.Total != 2 || m.ValidOutputs != 1 || m.BusinessPasses != 1 || m.CompletionRate != 0.5 || m.BusinessPassRate != 0.5 || m.P50MS == nil || *m.P50MS != 10 || m.P95MS == nil || *m.P95MS != 100 || m.CostUSD != nil {
		t.Fatalf("metrics %+v", m)
	}
}
func TestPilotSimulationExecutesAllArmsWithUnknownProviderCosts(t *testing.T) {
	p := SimulatePilot(context.Background())
	if !p.Passed || len(p.Samples) != 80 || p.FixtureInvocations != 215 || p.ProviderCalls != 0 {
		t.Fatalf("pilot status=%v samples=%d calls=%d", p.Passed, len(p.Samples), p.FixtureInvocations)
	}
	for _, m := range p.Metrics {
		if m.Total != 20 || m.BusinessPasses != 20 || m.CostUSD != nil || m.InputTokens != nil {
			t.Fatal("invented measurement")
		}
	}
}
