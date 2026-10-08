package governance

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type UsageBudget struct {
	MaxTokens          *int64
	MaxCostUnits       *int64
	RemainingTokens    *int64
	RemainingCostUnits *int64
}

func nullable(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copy := value.Int64
	return &copy
}

func checkedUsageAdd(total *int64, value int64) error {
	if value < 0 || *total > math.MaxInt64-value {
		return fail("GOAL_USAGE_OVERFLOW")
	}
	*total += value
	return nil
}

// RemainingUsageTx returns the cumulative allowance that can be frozen into a
// new Attempt. It deliberately walks ledger rows so SQLite SUM cannot overflow
// before Go has a chance to fail closed.
func RemainingUsageTx(ctx context.Context, q store.Query, goalID string) (UsageBudget, error) {
	var maxTokens, maxCost sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT max_tokens,max_cost_units FROM goal_budget_policy WHERE goal_id=?", goalID).Scan(&maxTokens, &maxCost)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageBudget{}, nil
	}
	if err != nil {
		return UsageBudget{}, err
	}
	out := UsageBudget{MaxTokens: nullable(maxTokens), MaxCostUnits: nullable(maxCost)}
	if !maxTokens.Valid && !maxCost.Valid {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT u.tokens,u.cost_units,u.complete
		FROM goal_attempt_reservations r
		LEFT JOIN goal_usage u ON u.attempt_id=r.attempt_id
		WHERE r.goal_id=? ORDER BY r.attempt_id`, goalID)
	if err != nil {
		return UsageBudget{}, err
	}
	defer rows.Close()
	var usedTokens, usedCost int64
	for rows.Next() {
		var tokens, cost, complete sql.NullInt64
		if err = rows.Scan(&tokens, &cost, &complete); err != nil {
			return UsageBudget{}, err
		}
		if !complete.Valid || complete.Int64 != 1 || !tokens.Valid || !cost.Valid {
			return UsageBudget{}, fail("GOAL_USAGE_UNKNOWN")
		}
		if err = checkedUsageAdd(&usedTokens, tokens.Int64); err != nil {
			return UsageBudget{}, err
		}
		if err = checkedUsageAdd(&usedCost, cost.Int64); err != nil {
			return UsageBudget{}, err
		}
	}
	if err = rows.Err(); err != nil {
		return UsageBudget{}, err
	}
	if maxTokens.Valid {
		if usedTokens >= maxTokens.Int64 {
			return UsageBudget{}, fail("GOAL_USAGE_EXHAUSTED")
		}
		remaining := maxTokens.Int64 - usedTokens
		out.RemainingTokens = &remaining
	}
	if maxCost.Valid {
		if usedCost >= maxCost.Int64 {
			return UsageBudget{}, fail("GOAL_USAGE_EXHAUSTED")
		}
		remaining := maxCost.Int64 - usedCost
		out.RemainingCostUnits = &remaining
	}
	return out, nil
}
