package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/governance"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func humanGoalContext(ctx context.Context) context.Context {
	p, _ := rpcutil.PrincipalFrom(ctx)
	p.Identity.GoalActorID = "alice"
	p.Identity.GoalActorKind = "human"
	p.Identity.Scopes = append(p.Identity.Scopes, "goals:approve", "goals:budget", "goals:constraints")
	return rpcutil.WithPrincipal(ctx, p)
}
func TestGoalGovernanceFencingReplayAndAbort(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer func() { s.Close() }()
	spec := (&jobHarness{commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).spec("single")
	j, err := s.SubmitJob(ctx, "governance", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	in := governance.Request{OperationID: "approve-1", ExpectedVersion: 1, PlanRevision: 1, GraphGeneration: 1, Action: "APPROVE_NEXT_REPLAN", Reason: "reviewed evidence"}
	if _, err = s.DecideGoal(ctx, j.ID, in); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary user approved: %v", err)
	}
	human := humanGoalContext(ctx)
	p, _ := rpcutil.PrincipalFrom(human)
	p.Identity.GoalActorKind = "planner"
	if _, err = s.DecideGoal(rpcutil.WithPrincipal(ctx, p), j.ID, in); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("planner approved: %v", err)
	}
	if _, err = s.db.SQL.Exec("UPDATE goals SET state='NEEDS_APPROVAL' WHERE id=?", "goal_"+j.ID); err != nil {
		t.Fatal(err)
	}
	stale := in
	stale.ExpectedVersion = 2
	if _, err = s.DecideGoal(human, j.ID, stale); status.Code(err) != codes.Aborted {
		t.Fatalf("stale approved: %v", err)
	}
	receipt, err := s.DecideGoal(human, j.ID, in)
	if err != nil || receipt.Version != 2 {
		t.Fatalf("approval=%+v %v", receipt, err)
	}
	cfg := s.cfg
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.DecideGoal(human, j.ID, in)
	if err != nil || !replay.Existing || replay.Version != 2 {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	conflict := in
	conflict.Reason = "different"
	if _, err = s.DecideGoal(human, j.ID, conflict); status.Code(err) != codes.Aborted {
		t.Fatalf("conflict=%v", err)
	}
	abort := in
	abort.OperationID = "abort-1"
	abort.ExpectedVersion = 2
	abort.Action = "ABORT"
	if _, err = s.DecideGoal(human, j.ID, abort); err != nil {
		t.Fatal(err)
	}
	done, err := readJob(ctx, s.db.SQL, j.ID)
	if err != nil || done.State != "STOPPING" {
		t.Fatalf("abort did not stop Job: %+v %v", done, err)
	}
	later := in
	later.OperationID = "late"
	later.ExpectedVersion = 3
	if _, err = s.DecideGoal(human, j.ID, later); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("terminal approval=%v", err)
	}
	if _, err = s.DecideGoal(human, j.ID, abort); err != nil {
		t.Fatalf("terminal replay=%v", err)
	}
}
func TestGoalGovernanceBudgetUsageAndRollback(t *testing.T) {
	s, ctx, _, peer := offlineJobServer(t)
	defer s.Close()
	j, err := s.SubmitJob(ctx, "budget", job.JSON((&jobHarness{commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	gid := "goal_" + j.ID
	children, err := jobChildren(ctx, s.db.SQL, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.assign(ctx, children[0].id, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var aid string
	if err = s.db.SQL.QueryRow("SELECT attempt FROM tasks WHERE id=?", children[0].id).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = s.db.SQL.QueryRow("SELECT version FROM goals WHERE id=?", gid).Scan(&version); err != nil {
		t.Fatal(err)
	}
	tokens := int64(100)
	in := governance.Request{OperationID: "budget-1", ExpectedVersion: version, PlanRevision: 1, GraphGeneration: 1, Action: "INCREASE_BUDGET", Reason: "approved fixed limit", Change: governance.Change{MaxTokens: &tokens}}
	human := humanGoalContext(ctx)
	p, _ := rpcutil.PrincipalFrom(human)
	p.Identity.Scopes = []string{"jobs:read", "goals:approve"}
	if _, err = s.DecideGoal(rpcutil.WithPrincipal(ctx, p), j.ID, in); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthorized budget=%v", err)
	}
	if _, err = s.DecideGoal(human, j.ID, in); err != nil {
		t.Fatal(err)
	}
	if err = governance.CheckUsageTx(ctx, s.db.SQL, gid); err == nil || err.Error() != "GOAL_USAGE_UNKNOWN" {
		t.Fatalf("missing usage=%v", err)
	}
	usage := governance.Usage{ID: "usage-1", AttemptID: aid, Source: "fixture", Complete: false}
	crash := errors.New("rollback")
	err = s.db.Tx(ctx, func(q store.Query) error {
		if e := governance.RecordUsageTx(ctx, q, gid, usage); e != nil {
			return e
		}
		return crash
	})
	if !errors.Is(err, crash) {
		t.Fatal(err)
	}
	var n int
	if err = s.db.SQL.QueryRow("SELECT count(*) FROM goal_usage").Scan(&n); err != nil || n != 0 {
		t.Fatalf("usage rollback=%d %v", n, err)
	}
	cost := int64(7)
	usage.Tokens = &tokens
	usage.CostUnits = &cost
	usage.Complete = true
	if err = s.db.Tx(ctx, func(q store.Query) error { return governance.RecordUsageTx(ctx, q, gid, usage) }); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Tx(ctx, func(q store.Query) error { return governance.RecordUsageTx(ctx, q, gid, usage) }); err != nil {
		t.Fatal(err)
	}
	if err = governance.CheckUsageTx(ctx, s.db.SQL, gid); err == nil || err.Error() != "GOAL_USAGE_EXHAUSTED" {
		t.Fatalf("exhausted usage=%v", err)
	}
	usage.Complete = false
	if err = s.db.Tx(ctx, func(q store.Query) error { return governance.RecordUsageTx(ctx, q, gid, usage) }); err == nil || err.Error() != "USAGE_CONFLICT" {
		t.Fatalf("usage conflict=%v", err)
	}
}

func TestGoalGovernanceProjectsOnlySupportedRuntimeBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		profile   string
		tokens    any
		cost      any
		supported bool
	}{
		{name: "Claude HTTP cost", profile: "claude_http", cost: int64(100), supported: true},
		{name: "Codex cost", profile: "codex_exec", cost: int64(100)},
		{name: "Claude token", profile: "claude_http", tokens: int64(100)},
		{name: "Claude mixed", profile: "claude_http", tokens: int64(100), cost: int64(100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx, _, _ := offlineJobServer(t)
			defer s.Close()
			j, err := s.SubmitJob(ctx, "projection", job.JSON((&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")))
			if err != nil {
				t.Fatal(err)
			}
			gid := "goal_" + j.ID
			if _, err = s.db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_tokens,max_cost_units) VALUES(?,?,?)", gid, tc.tokens, tc.cost); err != nil {
				t.Fatal(err)
			}
			current, err := readJob(ctx, s.db.SQL, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.frozen.Spec.Execution.Engine = ""
			current.frozen.Spec.Execution.RuntimeProfile = tc.profile
			if _, err = s.db.SQL.Exec("UPDATE jobs SET spec=? WHERE id=?", job.JSON(current.frozen), j.ID); err != nil {
				t.Fatal(err)
			}
			projection, err := goalGovernanceProjection(ctx, s.db.SQL, gid)
			if err != nil {
				t.Fatal(err)
			}
			if projection["runtime_hard_budget_supported"] != tc.supported {
				t.Fatalf("projection=%+v", projection)
			}
			capabilities, ok := projection["runtime_budget_capabilities"].([]string)
			if !ok || tc.supported && (len(capabilities) != 1 || capabilities[0] != claudeEstimatedUSDBudgetCapability) || !tc.supported && len(capabilities) != 0 {
				t.Fatalf("capabilities=%#v", projection["runtime_budget_capabilities"])
			}
		})
	}
}
