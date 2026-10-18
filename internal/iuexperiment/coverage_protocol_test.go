package iuexperiment

import (
	"context"
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"strings"
	"testing"
)

func TestCoverageProtocolPinsCasesAndAllArmDenominators(t *testing.T) {
	p, e := NewCoverageProtocol("fixture", "codex_exec", "fixture-1", "low")
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Samples) != 18 || p.MaxCalls < 72 {
		t.Fatal("coverage", len(p.Samples), p.MaxCalls)
	}
	p.Samples[0].ExpectedKnown = false
	if p.Validate() == nil {
		t.Fatal("mutable truth")
	}
}
func TestCoverageAblationsExposeFalseAcceptance(t *testing.T) {
	r := SimulateCoverage(context.Background())
	if len(r.Samples) != 72 || !r.Passed || r.ProviderCalls == nil || *r.ProviderCalls != 0 {
		t.Fatalf("%+v", r)
	}
	falseAccepts := 0
	for _, s := range r.Samples {
		if s.Arm == "iu_no_verifier" && s.FalseAcceptance {
			falseAccepts++
		}
		if s.InputTokens != nil || s.CostUSD != nil {
			t.Fatal("invented usage")
		}
	}
	if falseAccepts == 0 {
		t.Fatal("no injected false acceptance")
	}
}
func TestCoverageReportKeepsUnknownAndFailedRecovery(t *testing.T) {
	p, e := NewCoverageProtocol("fixture", "codex_exec", "fixture-1", "low")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := RunCoverageProtocol(ctx, p, func(context.Context, graph.ContextManifest) (Reply, error) {
		t.Fatal("cancelled call")
		return Reply{}, nil
	})
	if e != nil || len(r.Samples) != 72 {
		t.Fatal(e, len(r.Samples))
	}
	for _, s := range r.Samples {
		if s.Error != "NOT_ATTEMPTED" || s.Calls != 0 {
			t.Fatal(s)
		}
	}
}
func TestCoverageSimulationDoesNotLeakOracleToCalls(t *testing.T) {
	p, _ := NewCoverageProtocol("fixture", "codex_exec", "fixture-1", "low")
	calls := 0
	_, e := RunCoverageProtocol(context.Background(), p, func(_ context.Context, m graph.ContextManifest) (Reply, error) {
		calls++
		for _, bad := range []string{"expected_cents", "ground_truth", "injected_fault", "variant"} {
			if strings.Contains(m.Text, bad) {
				t.Fatal("oracle leak", bad)
			}
		}
		return Reply{Raw: json.RawMessage(`{"version":"iu-node-output-v1","values":{"result":{}},"records":[]}`)}, nil
	})
	if e != nil || calls == 0 {
		t.Fatal(e, calls)
	}
}
func TestCoverageMemoryMatchesActualInvocation(t *testing.T) {
	p, _ := NewCoverageProtocol("fixture", "codex_exec", "fixture-1", "low")
	known := map[string]bool{}
	fixture := coverageFixtureCall(p)
	_, e := RunCoverageProtocol(context.Background(), p, func(ctx context.Context, m graph.ContextManifest) (Reply, error) {
		var body struct {
			Parameters coverageCallParameters `json:"parameters"`
			Memory     graph.MemoryView       `json:"memory"`
		}
		json.Unmarshal([]byte(m.Text), &body)
		if body.Parameters.Stage == "extract" {
			known[m.SHA256] = true
		}
		for _, record := range body.Memory.Records {
			if !known[record.InputHash] {
				t.Fatalf("memory references non-input hash %s", record.InputHash)
			}
		}
		return fixture(ctx, m)
	})
	if e != nil {
		t.Fatal(e)
	}
}
