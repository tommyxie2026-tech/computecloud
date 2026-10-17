package iuexperiment

import (
	"context"
	"testing"
)

func TestSimulationChecksHealthyAndInjectedFailures(t *testing.T) {
	report := Simulate(context.Background())
	if report.RealModelCalls || !report.Passed {
		t.Fatalf("invalid simulation report %+v", report)
	}
	healthy, faults := 0, 0
	for _, s := range report.Scenarios {
		if !s.Passed {
			t.Fatalf("undetected scenario %+v", s)
		}
		if s.Fault == "none" {
			healthy++
		} else {
			faults++
		}
		if s.Sample.InputTokens != nil || s.Sample.CostUSD != nil {
			t.Fatal("invented model measurements")
		}
	}
	if healthy != 8 || faults < 8 {
		t.Fatalf("matrix incomplete healthy=%d fault=%d", healthy, faults)
	}
}
