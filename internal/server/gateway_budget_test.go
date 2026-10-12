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
	remaining, err := remainingAttemptTokens(context.Background(), db, "a", 100)
	if err != nil || remaining != 50 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_requests VALUES('a',0,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := remainingAttemptTokens(context.Background(), db, "a", 100); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unknown usage did not fail closed: %v", err)
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
