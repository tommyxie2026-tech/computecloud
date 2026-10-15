package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
)

func TestAttemptTokenLimitInjectsAndClampsOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fields    map[string]json.RawMessage
		remaining int64
		wantMax   int64
	}{
		{name: "inject default", fields: map[string]json.RawMessage{"model": json.RawMessage(`"m"`), "input": json.RawMessage(`"hello"`)}, remaining: 1000},
		{name: "respect smaller output", fields: map[string]json.RawMessage{"model": json.RawMessage(`"m"`), "input": json.RawMessage(`"hello"`), "max_output_tokens": json.RawMessage(`100`)}, remaining: 1000, wantMax: 100},
		{name: "clamp output", fields: map[string]json.RawMessage{"model": json.RawMessage(`"m"`), "input": json.RawMessage(`"hello"`), "max_output_tokens": json.RawMessage(`5000`)}, remaining: 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.fields)
			wantMax := tc.wantMax
			if wantMax == 0 {
				wantMax = tc.remaining - int64(len(body)) - 64
			}
			bounded, err := applyAttemptTokenLimit(tc.fields, body, tc.remaining, false)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(bounded, &got); err != nil {
				t.Fatal(err)
			}
			var max int64
			if err := json.Unmarshal(got["max_output_tokens"], &max); err != nil || max != wantMax {
				t.Fatalf("max_output_tokens=%d want=%d err=%v body=%s", max, wantMax, err, bounded)
			}
			if int64(len(bounded))+max > tc.remaining {
				t.Fatalf("bounded request exceeds remaining budget: bytes=%d output=%d remaining=%d", len(bounded), max, tc.remaining)
			}
		})
	}
}

func TestRemainingAttemptTokensUsesUsageAndFailsClosedOnUnknown(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE gateway_requests(attempt_id TEXT, usage_complete INTEGER, input_tokens INTEGER, output_tokens INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_requests VALUES('a',1,40,10)`); err != nil {
		t.Fatal(err)
	}
	remaining, err := remainingAttemptBudget(context.Background(), db, "a", 100, 100_000, 1_000_000, 2_000_000)
	if err != nil || remaining.tokens != 50 {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_requests VALUES('a',0,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := remainingAttemptBudget(context.Background(), db, "a", 100, 100_000, 1_000_000, 2_000_000); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unknown usage did not fail closed: %v", err)
	}
}

func TestRemainingAttemptBudgetPricesSettledUsageAndBoundsOutput(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE gateway_requests(attempt_id TEXT, usage_complete INTEGER, input_tokens INTEGER, output_tokens INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_requests VALUES('a',1,1000,100)`); err != nil {
		t.Fatal(err)
	}
	budget, err := remainingAttemptBudget(context.Background(), db, "a", 10_000, 100_000, 2_000_000, 10_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if budget.tokens != 8_900 || budget.costMicros != 97_000 {
		t.Fatalf("remaining budget=%+v, want 8900 tokens and 97000 micro-USD", budget)
	}
	fields := map[string]json.RawMessage{"model": json.RawMessage(`"m"`), "input": json.RawMessage(`"hello"`)}
	body, _ := json.Marshal(fields)
	bounded, err := applyAttemptBudget(fields, body, budget, 2_000_000, 10_000_000, false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	_ = json.Unmarshal(bounded, &got)
	var maxOutput int64
	_ = json.Unmarshal(got["max_output_tokens"], &maxOutput)
	if maxOutput < 1 || maxOutput > (budget.costMicros*1_000_000)/10_000_000 {
		t.Fatalf("output limit %d exceeds remaining dollar budget %d", maxOutput, budget.costMicros)
	}
}

func TestAttemptBudgetRejectsUnknownUsageAndOverflow(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE gateway_requests(attempt_id TEXT, usage_complete INTEGER, input_tokens INTEGER, output_tokens INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_requests VALUES('a',0,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := remainingAttemptBudget(context.Background(), db, "a", 1000, 100_000, 2_000_000, 10_000_000); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unknown usage was admitted: %v", err)
	}
	if got, err := tokenCostMicros(100, 10_000_000); err != nil || got != 1000 {
		t.Fatalf("cost=%d err=%v, want 1000 micro-USD", got, err)
	}
	if _, err := tokenCostMicros(100_000_000, int64(^uint64(0)>>1)); err == nil {
		t.Fatal("cost overflow was accepted")
	}
}

func TestAttemptTokenBudgetSerializesRequestsPerAttempt(t *testing.T) {
	g := &modelGateway{attemptSlots: map[string]chan struct{}{}}
	release, ok := g.acquireAttemptSlot("attempt-a")
	if !ok {
		t.Fatal("first Attempt request was not admitted")
	}
	if _, ok := g.acquireAttemptSlot("attempt-a"); ok {
		t.Fatal("concurrent request bypassed budget serialization")
	}
	release()
	releaseAgain, ok := g.acquireAttemptSlot("attempt-a")
	if !ok {
		t.Fatal("Attempt slot not released")
	}
	releaseAgain()
}

func TestAttemptTokenLimitFailsClosed(t *testing.T) {
	fields := map[string]json.RawMessage{"model": json.RawMessage(`"m"`), "input": json.RawMessage(`"a large input"`)}
	body, _ := json.Marshal(fields)
	if _, err := applyAttemptTokenLimit(fields, body, int64(len(body)+10), false); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("near-exhausted budget err=%v", err)
	}
	if _, err := applyAttemptTokenLimit(fields, body, 1000, true); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("compact endpoint err=%v", err)
	}
	fields["max_output_tokens"] = json.RawMessage(`0`)
	if _, err := applyAttemptTokenLimit(fields, body, 1000, false); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid output limit err=%v", err)
	}
}

func TestAnthropicAttemptBudgetClampsMaxTokensWithoutOpenAIFields(t *testing.T) {
	fields := map[string]json.RawMessage{
		"model":      json.RawMessage(`"claude-safe"`),
		"max_tokens": json.RawMessage(`400`),
		"messages":   json.RawMessage(`[ {"role":"user","content":"hello"} ]`),
	}
	body, _ := json.Marshal(fields)
	bounded, err := applyAnthropicAttemptBudget(fields, body, attemptBudget{tokens: 200, costMicros: 1000}, 1_000_000, 2_000_000)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(bounded, &got); err != nil {
		t.Fatal(err)
	}
	var max int64
	if err := json.Unmarshal(got["max_tokens"], &max); err != nil || max < 1 || max >= 400 {
		t.Fatalf("Anthropic max_tokens=%d err=%v body=%s", max, err, bounded)
	}
	if _, ok := got["max_output_tokens"]; ok {
		t.Fatalf("budget helper injected a Responses field into Anthropic JSON: %s", bounded)
	}
}
