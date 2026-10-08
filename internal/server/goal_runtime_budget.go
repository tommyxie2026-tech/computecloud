package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/governance"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

const claudeEstimatedUSDBudgetCapability = "runtime:budget_claude_estimated_usd_v1"
const claudeEstimatedUSDMicros = "usd_micros_client_estimate"

func buildGoalRuntimeBudget(ctx context.Context, q store.Query, j *Job, t *pb.Task) (*pb.RuntimeBudget, []string, error) {
	if j == nil {
		return nil, nil, nil
	}
	goalID, err := ensureGoalBinding(ctx, q, j)
	if err != nil {
		return nil, nil, err
	}
	var maxTokens, maxCost sql.NullInt64
	err = q.QueryRowContext(ctx, "SELECT max_tokens,max_cost_units FROM goal_budget_policy WHERE goal_id=?", goalID).Scan(&maxTokens, &maxCost)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !maxTokens.Valid && !maxCost.Valid {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if maxTokens.Valid || !maxCost.Valid || t == nil || t.Spec == nil || t.Spec.RuntimeProfile != "claude_http" {
		return nil, nil, fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
	}
	for _, execution := range j.frozen.Spec.Executions() {
		if execution.RuntimeProfile != "claude_http" {
			return nil, nil, fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
		}
	}
	var active int
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM goal_attempt_reservations r
		JOIN attempts a ON a.id=r.attempt_id WHERE r.goal_id=? AND a.released=0`, goalID).Scan(&active); err != nil {
		return nil, nil, err
	}
	if active != 0 {
		return nil, nil, fmt.Errorf("GOAL_BUDGET_IN_FLIGHT")
	}
	remaining, err := governance.RemainingUsageTx(ctx, q, goalID)
	if err != nil {
		return nil, nil, err
	}
	return &pb.RuntimeBudget{
		RemainingCostUnits: remaining.RemainingCostUnits,
		CostSemantics:      claudeEstimatedUSDMicros,
	}, []string{claudeEstimatedUSDBudgetCapability}, nil
}

func goalRuntimeBudgetSupportedForSpec(ctx context.Context, q store.Query, goalID string, spec job.Spec) error {
	var maxTokens, maxCost sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT max_tokens,max_cost_units FROM goal_budget_policy WHERE goal_id=?", goalID).Scan(&maxTokens, &maxCost)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !maxTokens.Valid && !maxCost.Valid {
		return nil
	}
	if maxTokens.Valid || !maxCost.Valid {
		return fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
	}
	for _, execution := range spec.Executions() {
		if execution.RuntimeProfile != "claude_http" {
			return fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
		}
	}
	return nil
}
