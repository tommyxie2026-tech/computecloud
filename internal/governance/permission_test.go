package governance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/governance"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func permissionFixture(t *testing.T) (*store.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(t.TempDir(), store.ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := goal.Create(ctx, db, goal.CreateGoal{
		ID: "goal-1", Owner: "owner", Project: "project",
		Budget: goal.Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(ctx, "UPDATE goals SET state='NEEDS_APPROVAL' WHERE id='goal-1'"); err != nil {
		t.Fatal(err)
	}
	actor := governance.Actor{ID: "human-1", Kind: "human", Owner: "owner", Projects: []string{"project"}, Scopes: []string{"goals:approve"}}
	in := governance.Request{OperationID: "approval-1", ExpectedVersion: 1, PlanRevision: 1, GraphGeneration: 1, Action: "APPROVE_NEXT_REPLAN", Reason: "reviewed evidence"}
	if err := db.Tx(ctx, func(q store.Query) error {
		_, err := governance.DecideTx(ctx, q, actor, "goal-1", in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

func consumed(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	if err := db.SQL.QueryRow("SELECT consumed FROM goal_replan_permissions WHERE goal_id='goal-1' AND operation_id='approval-1'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReplanPermissionIsOneUseAndGenerationFenced(t *testing.T) {
	db, ctx := permissionFixture(t)
	for _, tc := range []struct {
		operation            string
		revision, generation int64
	}{{"wrong-operation", 1, 1}, {"approval-1", 2, 1}, {"approval-1", 1, 2}} {
		err := db.Tx(ctx, func(q store.Query) error {
			return governance.ConsumeReplanPermissionTx(ctx, q, "goal-1", tc.operation, tc.revision, tc.generation)
		})
		if err == nil || err.Error() != "REPLAN_PERMISSION_UNAVAILABLE" || consumed(t, db) != 0 {
			t.Fatalf("invalid permission accepted: %+v err=%v", tc, err)
		}
	}
	if err := db.Tx(ctx, func(q store.Query) error {
		return governance.ConsumeReplanPermissionTx(ctx, q, "goal-1", "approval-1", 1, 1)
	}); err != nil || consumed(t, db) != 1 {
		t.Fatalf("permission not consumed once: err=%v", err)
	}
	err := db.Tx(ctx, func(q store.Query) error {
		return governance.ConsumeReplanPermissionTx(ctx, q, "goal-1", "approval-1", 1, 1)
	})
	if err == nil || err.Error() != "REPLAN_PERMISSION_UNAVAILABLE" || consumed(t, db) != 1 {
		t.Fatalf("permission replay accepted: %v", err)
	}
}

func TestReplanPermissionRollsBackWithPublication(t *testing.T) {
	db, ctx := permissionFixture(t)
	crash := errors.New("publication failed")
	err := db.Tx(ctx, func(q store.Query) error {
		if err := governance.ConsumeReplanPermissionTx(ctx, q, "goal-1", "approval-1", 1, 1); err != nil {
			return err
		}
		return crash
	})
	if !errors.Is(err, crash) || consumed(t, db) != 0 {
		t.Fatalf("failed publication consumed permission: %v", err)
	}
	if _, err = db.SQL.ExecContext(ctx, "UPDATE goals SET active_graph_generation=2 WHERE id='goal-1'"); err != nil {
		t.Fatal(err)
	}
	err = db.Tx(ctx, func(q store.Query) error {
		return governance.ConsumeReplanPermissionTx(ctx, q, "goal-1", "approval-1", 1, 1)
	})
	if err == nil || err.Error() != "REPLAN_PERMISSION_UNAVAILABLE" || consumed(t, db) != 0 {
		t.Fatalf("stale generation consumed permission: %v", err)
	}
}
