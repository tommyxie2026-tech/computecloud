package adapter

import (
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"google.golang.org/protobuf/proto"
)

// A missing optional limit must stay distinguishable from a finite limit. If
// this breaks, an unbudgeted Assignment can silently become a zero budget (or a
// finite budget can be dropped) while crossing the Worker wire boundary.
func TestRuntimeBudgetWirePresenceAndCopy(t *testing.T) {
	one := int64(1)
	in := &pb.Assignment{RuntimeBudget: &pb.RuntimeBudget{
		RemainingCostUnits: &one,
		CostSemantics:      "usd_micros_client_estimate",
	}}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var decoded pb.Assignment
	if err = proto.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	budget := decoded.GetRuntimeBudget()
	if budget == nil || budget.RemainingTokenUnits != nil || budget.RemainingCostUnits == nil || budget.GetRemainingCostUnits() != 1 || budget.GetCostSemantics() != "usd_micros_client_estimate" {
		t.Fatalf("budget presence changed across wire: %+v", budget)
	}

	var unlimited pb.Assignment
	raw, err = proto.Marshal(&pb.Assignment{})
	if err != nil {
		t.Fatal(err)
	}
	if err = proto.Unmarshal(raw, &unlimited); err != nil {
		t.Fatal(err)
	}
	if unlimited.GetRuntimeBudget() != nil {
		t.Fatalf("absent budget became present: %+v", unlimited.GetRuntimeBudget())
	}

	prepared, err := prepareCLI(httpRuntimeProvider{profile: "claude_http", cli: claudeProvider{}}, PrepareRequest{
		Spec:   &pb.TaskSpec{Model: "fixture-model"},
		Policy: config.Policy{ClaudePermissionMode: "dontAsk"},
		Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	*budget.RemainingCostUnits = 2
	if prepared.Budget == nil || prepared.Budget.GetRemainingCostUnits() != 1 {
		t.Fatalf("prepared execution aliases mutable assignment budget: %+v", prepared.Budget)
	}
}
