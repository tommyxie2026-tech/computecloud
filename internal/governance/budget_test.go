package governance

import (
	"context"
	"math"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func newBudgetDB(t *testing.T) (*store.DB, string) {
	t.Helper()
	db, err := store.Open(t.TempDir(), store.ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	goalID := "goal-budget"
	if _, err = db.SQL.Exec(`INSERT INTO goals(id,owner,project,state,max_replans,max_total_attempts,max_wall_time_ms,created,updated)
		VALUES(?,'owner','project','RUNNING',0,100,100000,1,1)`, goalID); err != nil {
		t.Fatal(err)
	}
	return db, goalID
}

func addBudgetUsage(t *testing.T, db *store.DB, goalID, suffix string, tokens, cost *int64, complete bool) {
	t.Helper()
	taskID, attemptID := "task-"+suffix, "attempt-"+suffix
	if _, err := db.SQL.Exec(`INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline)
		VALUES(?,'owner','project',?,?,'{}','FAILED',1,1,1000)`, taskID, suffix, suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released)
		VALUES(?,?,'worker','epoch',1,?,1000,1)`, attemptID, taskID, suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO goal_attempt_reservations(attempt_id,goal_id,plan_revision,graph_generation,created)
		VALUES(?,?,1,1,1)`, attemptID, goalID); err != nil {
		t.Fatal(err)
	}
	if tokens == nil && cost == nil && !complete {
		return
	}
	if _, err := db.SQL.Exec(`INSERT INTO goal_usage(goal_id,usage_id,attempt_id,source,tokens,cost_units,complete,request_hash,created)
		VALUES(?,?,?,'fixture',?,?,?,?,1)`, goalID, "usage-"+suffix, attemptID, tokens, cost, complete, suffix); err != nil {
		t.Fatal(err)
	}
}

func TestRemainingUsageTx(t *testing.T) {
	t.Run("no policy", func(t *testing.T) {
		db, goalID := newBudgetDB(t)
		got, err := RemainingUsageTx(context.Background(), db.SQL, goalID)
		if err != nil || got.MaxTokens != nil || got.MaxCostUnits != nil || got.RemainingTokens != nil || got.RemainingCostUnits != nil {
			t.Fatalf("budget=%+v err=%v", got, err)
		}
	})

	t.Run("cost only unused", func(t *testing.T) {
		db, goalID := newBudgetDB(t)
		if _, err := db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,100)", goalID); err != nil {
			t.Fatal(err)
		}
		got, err := RemainingUsageTx(context.Background(), db.SQL, goalID)
		if err != nil || got.RemainingCostUnits == nil || *got.RemainingCostUnits != 100 || got.RemainingTokens != nil {
			t.Fatalf("budget=%+v err=%v", got, err)
		}
	})

	t.Run("accumulated attempts", func(t *testing.T) {
		db, goalID := newBudgetDB(t)
		if _, err := db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,100)", goalID); err != nil {
			t.Fatal(err)
		}
		zero, thirty, twenty := int64(0), int64(30), int64(20)
		addBudgetUsage(t, db, goalID, "one", &zero, &thirty, true)
		addBudgetUsage(t, db, goalID, "two", &zero, &twenty, true)
		got, err := RemainingUsageTx(context.Background(), db.SQL, goalID)
		if err != nil || got.RemainingCostUnits == nil || *got.RemainingCostUnits != 50 {
			t.Fatalf("budget=%+v err=%v", got, err)
		}
	})

	for _, tc := range []struct {
		name string
		used int64
	}{
		{"equal exhausted", 100},
		{"above exhausted", 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, goalID := newBudgetDB(t)
			if _, err := db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,100)", goalID); err != nil {
				t.Fatal(err)
			}
			zero := int64(0)
			addBudgetUsage(t, db, goalID, "used", &zero, &tc.used, true)
			if _, err := RemainingUsageTx(context.Background(), db.SQL, goalID); err == nil || err.Error() != "GOAL_USAGE_EXHAUSTED" {
				t.Fatalf("err=%v", err)
			}
		})
	}

	t.Run("missing or incomplete", func(t *testing.T) {
		for _, incomplete := range []bool{false, true} {
			db, goalID := newBudgetDB(t)
			if _, err := db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,100)", goalID); err != nil {
				t.Fatal(err)
			}
			if incomplete {
				zero := int64(0)
				addBudgetUsage(t, db, goalID, "unknown", &zero, nil, false)
			} else {
				addBudgetUsage(t, db, goalID, "missing", nil, nil, false)
			}
			if _, err := RemainingUsageTx(context.Background(), db.SQL, goalID); err == nil || err.Error() != "GOAL_USAGE_UNKNOWN" {
				t.Fatalf("incomplete=%v err=%v", incomplete, err)
			}
		}
	})

	t.Run("nullable dimension", func(t *testing.T) {
		db, goalID := newBudgetDB(t)
		if _, err := db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_tokens) VALUES(?,100)", goalID); err != nil {
			t.Fatal(err)
		}
		tokens, cost := int64(25), int64(999)
		addBudgetUsage(t, db, goalID, "tokens", &tokens, &cost, true)
		got, err := RemainingUsageTx(context.Background(), db.SQL, goalID)
		if err != nil || got.RemainingTokens == nil || *got.RemainingTokens != 75 || got.RemainingCostUnits != nil {
			t.Fatalf("budget=%+v err=%v", got, err)
		}
	})
}

func TestUsageOverflowFailsClosed(t *testing.T) {
	db, goalID := newBudgetDB(t)
	if _, err := db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,?)", goalID, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	zero, huge, two := int64(0), int64(math.MaxInt64-1), int64(2)
	addBudgetUsage(t, db, goalID, "huge", &zero, &huge, true)
	addBudgetUsage(t, db, goalID, "overflow", &zero, &two, true)
	if _, err := RemainingUsageTx(context.Background(), db.SQL, goalID); err == nil || err.Error() != "GOAL_USAGE_OVERFLOW" {
		t.Fatalf("overflow err=%v", err)
	}
}
