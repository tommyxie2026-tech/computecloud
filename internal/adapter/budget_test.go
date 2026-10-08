package adapter

import (
	"encoding/json"
	"reflect"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
)

func int64ptr(value int64) *int64 { return &value }

func TestFormatUSDmicros(t *testing.T) {
	for _, tc := range []struct {
		units int64
		want  string
	}{
		{1, "0.000001"},
		{1_250_000, "1.250000"},
		{2_000_000_000_000_000, "2000000000.000000"},
	} {
		got, err := formatUSDmicros(tc.units)
		if err != nil || got != tc.want {
			t.Fatalf("formatUSDmicros(%d)=%q,%v want %q", tc.units, got, err, tc.want)
		}
	}
	for _, units := range []int64{0, -1, 2_000_000_000_000_001} {
		if got, err := formatUSDmicros(units); err == nil {
			t.Fatalf("formatUSDmicros(%d) accepted as %q", units, got)
		}
	}
}

func TestParseUSDToMicrosRoundsUpWithoutFloat(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"0", 0},
		{"0.000001", 1},
		{"1.250000", 1_250_000},
		{"1e-6", 1},
		{"1.2345671", 1_234_568},
		{"2000000000", 2_000_000_000_000_000},
	} {
		got, err := parseUSDmicros(json.RawMessage(tc.raw))
		if err != nil || got != tc.want {
			t.Fatalf("parseUSDmicros(%q)=%d,%v want %d", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"-0.1", "NaN", "1e999", "2000000000.000001", `"1.0"`} {
		if got, err := parseUSDmicros(json.RawMessage(raw)); err == nil {
			t.Fatalf("parseUSDmicros(%q) accepted as %d", raw, got)
		}
	}
}

func TestApplyHTTPRuntimeBudgetRejectsUnsupportedDimensions(t *testing.T) {
	valid := &pb.RuntimeBudget{RemainingCostUnits: int64ptr(1_250_000), CostSemantics: "usd_micros_client_estimate"}
	got, err := applyHTTPRuntimeBudget("claude_http", []string{"-p"}, valid)
	if err != nil || !reflect.DeepEqual(got, []string{"-p", "--max-budget-usd", "1.250000"}) {
		t.Fatalf("valid budget args=%v err=%v", got, err)
	}
	for _, tc := range []struct {
		profile string
		budget  *pb.RuntimeBudget
	}{
		{"codex_http", valid},
		{"claude_print", valid},
		{"claude_http", &pb.RuntimeBudget{RemainingTokenUnits: int64ptr(1)}},
		{"claude_http", &pb.RuntimeBudget{RemainingCostUnits: int64ptr(1), CostSemantics: "provider_invoice"}},
	} {
		if args, err := applyHTTPRuntimeBudget(tc.profile, []string{"fixture"}, tc.budget); err == nil {
			t.Fatalf("unsupported budget accepted for %s: %v", tc.profile, args)
		}
	}
}

func TestClaudeBudgetResultAllowsOneCallOvershoot(t *testing.T) {
	p := Parser{Profile: "claude_print", Emit: func(string, []byte) error { return nil }}
	raw := []byte(`{"type":"result","subtype":"error_max_budget_usd","is_error":true,"result":"stopped","total_cost_usd":0.0100001,"usage":{"input_tokens":3,"output_tokens":4}}`)
	if err := p.Line(raw); err != nil {
		t.Fatal(err)
	}
	out := p.Outcome()
	if !out.Final || out.Success || !out.BudgetReached || out.Code != "RUNTIME_BUDGET_EXHAUSTED" || !out.UsageComplete || !out.CostComplete || out.CostUnits == nil || *out.CostUnits != 10_001 {
		t.Fatalf("budget outcome=%+v", out)
	}
}
