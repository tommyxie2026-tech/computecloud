package goal

import (
	"context"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func newGoalDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir(), store.ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createTestGoal(t *testing.T, db *store.DB, id string, budget Budget) {
	t.Helper()
	if err := Create(context.Background(), db, CreateGoal{
		ID: id, Owner: "owner", Project: "project", Budget: budget,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGuardReplanAllowsAndAdvancesGeneration(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})

	got, err := GuardReplan(context.Background(), db, ReplanRequest{
		ID: "r1", GoalID: "g", EvaluationID: "e1",
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allowed || got.Code != DecisionAllowed || got.NextPlanRevision != 2 || got.NextGraphGeneration != 2 {
		t.Fatalf("unexpected decision: %+v", got)
	}

	var plan, graph int64
	var replans int
	if err = db.SQL.QueryRow("SELECT active_plan_revision,active_graph_generation,consumed_replans FROM goals WHERE id='g'").Scan(&plan, &graph, &replans); err != nil {
		t.Fatal(err)
	}
	if plan != 2 || graph != 2 || replans != 1 {
		t.Fatalf("goal state plan=%d graph=%d replans=%d", plan, graph, replans)
	}
}

func TestGuardReplanIsIdempotentPerEvaluation(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	req := ReplanRequest{
		ID: "r1", GoalID: "g", EvaluationID: "e1",
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
	}
	first, err := GuardReplan(context.Background(), db, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GuardReplan(context.Background(), db, ReplanRequest{
		ID: "different-delivery-id", GoalID: "g", EvaluationID: "e1",
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Allowed || !second.Allowed || !second.Existing ||
		first.NextPlanRevision != second.NextPlanRevision ||
		first.NextGraphGeneration != second.NextGraphGeneration {
		t.Fatalf("idempotency failed first=%+v second=%+v", first, second)
	}
	var n int
	if err = db.SQL.QueryRow("SELECT consumed_replans FROM goals WHERE id='g'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("consumed_replans=%d", n)
	}
}

func TestGuardReplanRejectsStaleGeneration(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	if _, err := GuardReplan(context.Background(), db, ReplanRequest{
		ID: "r1", GoalID: "g", EvaluationID: "e1",
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := GuardReplan(context.Background(), db, ReplanRequest{
		ID: "r2", GoalID: "g", EvaluationID: "late-eval",
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || got.NeedsApproval || got.Code != DecisionStaleGeneration {
		t.Fatalf("stale generation accepted: %+v", got)
	}
}

func TestGuardReplanStopsAtMaxReplans(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 1, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	first, err := GuardReplan(context.Background(), db, ReplanRequest{
		ID: "r1", GoalID: "g", EvaluationID: "e1",
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
	})
	if err != nil || !first.Allowed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err = db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}
	second, err := GuardReplan(context.Background(), db, ReplanRequest{
		ID: "r2", GoalID: "g", EvaluationID: "e2",
		ExpectedPlanRevision: 2, ExpectedGraphGeneration: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Allowed || !second.NeedsApproval || second.Code != DecisionBudgetExhausted {
		t.Fatalf("budget guard failed: %+v", second)
	}
}

func TestReserveAttemptStopsAtGoalBudget(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 2, MaxWallTime: time.Hour})
	if err := ReserveAttempt(context.Background(), db, "g"); err != nil {
		t.Fatal(err)
	}
	if err := ReserveAttempt(context.Background(), db, "g"); err != nil {
		t.Fatal(err)
	}
	if err := ReserveAttempt(context.Background(), db, "g"); err == nil || err.Error() != DecisionAttemptExhausted {
		t.Fatalf("third attempt err=%v", err)
	}
}

func TestGuardReplanStopsAfterDeadline(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{
		MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour,
		DeadlineMS: store.Now() - 1,
	})
	got, err := GuardReplan(context.Background(), db, ReplanRequest{
		ID: "r1", GoalID: "g", EvaluationID: "e1",
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionDeadlineExceeded {
		t.Fatalf("deadline guard failed: %+v", got)
	}
}
