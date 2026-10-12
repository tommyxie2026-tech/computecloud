package server

import (
	"context"
	"encoding/json"

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

func remainingAttemptTokens(ctx context.Context, q store.Query, attemptID string, limit int64) (int64, error) {
	if attemptID == "" || limit < 1 {
		return 0, status.Error(codes.FailedPrecondition, "ATTEMPT_TOKEN_BUDGET_UNCONFIGURED")
	}
	var unresolved int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM gateway_requests
		WHERE attempt_id=? AND (usage_complete=0 OR input_tokens IS NULL OR output_tokens IS NULL)`, attemptID).Scan(&unresolved); err != nil {
		return 0, err
	}
	if unresolved != 0 {
		return 0, status.Error(codes.FailedPrecondition, "ATTEMPT_TOKEN_USAGE_UNKNOWN")
	}
	var used int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens+output_tokens),0)
		FROM gateway_requests WHERE attempt_id=? AND usage_complete=1`, attemptID).Scan(&used); err != nil {
		return 0, err
	}
	if used < 0 || used >= limit {
		return 0, status.Error(codes.ResourceExhausted, "ATTEMPT_TOKEN_BUDGET_EXHAUSTED")
	}
	return limit - used, nil
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
