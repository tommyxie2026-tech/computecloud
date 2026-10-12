package server

import (
	"context"
	"encoding/json"
	"math/big"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (g *modelGateway) acquireAttemptSlot(attemptID string) (func(), bool) {
	g.attemptMu.Lock()
	slot := g.attemptSlots[attemptID]
	if slot == nil {
		slot = make(chan struct{}, 1)
		g.attemptSlots[attemptID] = slot
	}
	select {
	case slot <- struct{}{}:
		g.attemptMu.Unlock()
		return func() {
			g.attemptMu.Lock()
			<-slot
			if len(slot) == 0 && g.attemptSlots[attemptID] == slot {
				delete(g.attemptSlots, attemptID)
			}
			g.attemptMu.Unlock()
		}, true
	default:
		g.attemptMu.Unlock()
		return nil, false
	}
}

type attemptBudget struct {
	tokens     int64
	costMicros int64
}

func remainingAttemptBudget(ctx context.Context, q store.Query, attemptID string, tokenLimit, costLimit, inputPrice, outputPrice int64) (attemptBudget, error) {
	if attemptID == "" || tokenLimit < 1 || costLimit < 1 || inputPrice < 1 || outputPrice < 1 {
		return attemptBudget{}, status.Error(codes.FailedPrecondition, "ATTEMPT_BUDGET_UNCONFIGURED")
	}
	var unresolved int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM gateway_requests
		WHERE attempt_id=? AND (usage_complete=0 OR input_tokens IS NULL OR output_tokens IS NULL)`, attemptID).Scan(&unresolved); err != nil {
		return attemptBudget{}, err
	}
	if unresolved != 0 {
		return attemptBudget{}, status.Error(codes.FailedPrecondition, "ATTEMPT_USAGE_UNKNOWN")
	}
	rows, err := q.QueryContext(ctx, `SELECT input_tokens,output_tokens FROM gateway_requests WHERE attempt_id=? AND usage_complete=1`, attemptID)
	if err != nil {
		return attemptBudget{}, err
	}
	var usedTokens, usedCost int64
	for rows.Next() {
		var input, output int64
		if err := rows.Scan(&input, &output); err != nil {
			rows.Close()
			return attemptBudget{}, err
		}
		if input < 0 || output < 0 {
			rows.Close()
			return attemptBudget{}, status.Error(codes.FailedPrecondition, "ATTEMPT_USAGE_INVALID")
		}
		var ok bool
		usedTokens, ok = addNonNegative(usedTokens, input)
		if !ok {
			rows.Close()
			return attemptBudget{}, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
		}
		usedTokens, ok = addNonNegative(usedTokens, output)
		if !ok {
			rows.Close()
			return attemptBudget{}, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
		}
		inputCost, err := tokenCostMicros(input, inputPrice)
		if err != nil {
			rows.Close()
			return attemptBudget{}, err
		}
		outputCost, err := tokenCostMicros(output, outputPrice)
		if err != nil {
			rows.Close()
			return attemptBudget{}, err
		}
		usedCost, ok = addNonNegative(usedCost, inputCost)
		if !ok {
			rows.Close()
			return attemptBudget{}, status.Error(codes.ResourceExhausted, "ATTEMPT_COST_BUDGET_EXHAUSTED")
		}
		usedCost, ok = addNonNegative(usedCost, outputCost)
		if !ok {
			rows.Close()
			return attemptBudget{}, status.Error(codes.ResourceExhausted, "ATTEMPT_COST_BUDGET_EXHAUSTED")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return attemptBudget{}, err
	}
	if err := rows.Close(); err != nil {
		return attemptBudget{}, err
	}
	if usedTokens >= tokenLimit {
		return attemptBudget{}, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
	}
	if usedCost >= costLimit {
		return attemptBudget{}, status.Error(codes.ResourceExhausted, "ATTEMPT_COST_BUDGET_EXHAUSTED")
	}
	return attemptBudget{tokens: tokenLimit - usedTokens, costMicros: costLimit - usedCost}, nil
}

func addNonNegative(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > int64(^uint64(0)>>1)-b {
		return 0, false
	}
	return a + b, true
}

func tokenCostMicros(tokens, pricePerMillionMicros int64) (int64, error) {
	if tokens < 0 || pricePerMillionMicros < 0 {
		return 0, status.Error(codes.FailedPrecondition, "ATTEMPT_USAGE_INVALID")
	}
	product := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(pricePerMillionMicros))
	product.Add(product, big.NewInt(999_999))
	product.Quo(product, big.NewInt(1_000_000))
	if !product.IsInt64() {
		return 0, status.Error(codes.ResourceExhausted, "ATTEMPT_COST_BUDGET_EXHAUSTED")
	}
	return product.Int64(), nil
}

func costTokenCapacity(costMicros, pricePerMillionMicros int64) int64 {
	if costMicros <= 0 || pricePerMillionMicros <= 0 {
		return 0
	}
	capacity := new(big.Int).Mul(big.NewInt(costMicros), big.NewInt(1_000_000))
	capacity.Quo(capacity, big.NewInt(pricePerMillionMicros))
	if !capacity.IsInt64() || capacity.Int64() > int64(^uint64(0)>>1) {
		return int64(^uint64(0) >> 1)
	}
	return capacity.Int64()
}

func applyAttemptBudget(fields map[string]json.RawMessage, body []byte, remaining attemptBudget, inputPrice, outputPrice int64, compact bool) ([]byte, error) {
	bounded, err := applyAttemptTokenLimit(fields, body, remaining.tokens, compact)
	if err != nil {
		return nil, err
	}
	inputTokens := int64(len(body)) + 64
	inputCost, err := tokenCostMicros(inputTokens, inputPrice)
	if err != nil {
		return nil, err
	}
	if inputCost >= remaining.costMicros {
		return nil, status.Error(codes.ResourceExhausted, "ATTEMPT_COST_BUDGET_EXHAUSTED")
	}
	costOutputLimit := costTokenCapacity(remaining.costMicros-inputCost, outputPrice)
	if costOutputLimit < 1 {
		return nil, status.Error(codes.ResourceExhausted, "ATTEMPT_COST_BUDGET_EXHAUSTED")
	}
	var adjusted map[string]json.RawMessage
	if err := json.Unmarshal(bounded, &adjusted); err != nil {
		return nil, err
	}
	var tokenOutputLimit int64
	if err := json.Unmarshal(adjusted["max_output_tokens"], &tokenOutputLimit); err != nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_MAX_OUTPUT_TOKENS")
	}
	if costOutputLimit < tokenOutputLimit {
		tokenOutputLimit = costOutputLimit
		adjusted["max_output_tokens"], _ = json.Marshal(tokenOutputLimit)
		bounded = job.JSON(adjusted)
	}
	if int64(len(bounded))+tokenOutputLimit > remaining.tokens {
		return nil, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
	}
	return bounded, nil
}

func applyAttemptTokenLimit(fields map[string]json.RawMessage, body []byte, remaining int64, compact bool) ([]byte, error) {
	if compact {
		return nil, status.Error(codes.FailedPrecondition, "ATTEMPT_TOKEN_BUDGET_UNSUPPORTED_ENDPOINT")
	}
	if remaining < 1 {
		return nil, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
	}
	outputLimit := remaining - int64(len(body)) - 64
	if outputLimit < 1 {
		return nil, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
	}
	if outputLimit > 1_000_000 {
		outputLimit = 1_000_000
	}
	if raw, ok := fields["max_output_tokens"]; ok {
		var requested int64
		if err := json.Unmarshal(raw, &requested); err != nil || requested < 1 {
			return nil, status.Error(codes.InvalidArgument, "INVALID_MAX_OUTPUT_TOKENS")
		}
		if requested < outputLimit {
			outputLimit = requested
		}
	}
	rawLimit, err := json.Marshal(outputLimit)
	if err != nil {
		return nil, err
	}
	fields["max_output_tokens"] = rawLimit
	bounded := job.JSON(fields)
	if int64(len(bounded))+outputLimit > remaining {
		outputLimit -= int64(len(bounded)) + outputLimit - remaining
		if outputLimit < 1 {
			return nil, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
		}
		rawLimit, err = json.Marshal(outputLimit)
		if err != nil {
			return nil, err
		}
		fields["max_output_tokens"] = rawLimit
		bounded = job.JSON(fields)
	}
	if int64(len(bounded))+outputLimit > remaining {
		return nil, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
	}
	return bounded, nil
}
