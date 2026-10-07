package goal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/governance"
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
	if err := RegisterPlanFingerprint(context.Background(), db, id, 1, plan("initial")); err != nil {
		t.Fatal(err)
	}
}

func plan(strategy string) PlanCanonical {
	return PlanCanonical{
		Strategy: strategy, StrategyClass: strategy, DependencySignature: "scan>implement>test",
		RequiredCapabilities: []string{"runtime:codex", "tool:git"},
		KeyAssumptions:       []string{"repository accessible"},
		EvaluationStrategy:   "tests", SideEffectClass: "workspace",
	}
}

func TestPlanFingerprintBindsConcreteJobSpec(t *testing.T) {
	a := plan("strategy")
	b := a
	b.JobSpecHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("concrete Job spec did not change Plan fingerprint")
	}
	b.JobSpecHash = "invalid"
	if err := b.Validate(); err == nil {
		t.Fatal("invalid Job spec hash accepted")
	}
}

func request(id, eval string, planRev, graphGen int64, evidenceFact, strategy string) ReplanRequest {
	return ReplanRequest{
		ID: id, GoalID: "g", EvaluationID: eval,
		ExpectedPlanRevision: planRev, ExpectedGraphGeneration: graphGen,
		ReasonCode: "ASSUMPTION_INVALIDATED",
		Evidence: ReplanEvidence{
			FailureClass: FailureInvalidAssumption,
			Evidence:     []Evidence{{Type: "test_failure", ArtifactID: "artifact-" + eval, Fact: evidenceFact}},
		},
		ProposedPlan: plan(strategy),
		StrategyDelta: StrategyDelta{
			ChangedDimensions: []string{"strategy"},
			Summary:           "change execution strategy",
		},
		Progress: ProgressSnapshot{
			AcceptedChecks:      1,
			FailedChecks:        1,
			UnknownChecks:       1,
			ResolvedAssumptions: 1,
			UnresolvedBlockers:  1,
		},
	}
}

func publishTestPlan(ctx context.Context, q store.Query, d ReplanDecision) error {
	now := store.Now()
	jobID := fmt.Sprintf("replan-job-%d", d.NextPlanRevision)
	frozen := []byte(`{"spec":"frozen"}`)
	if _, err := q.ExecContext(ctx, `INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism)
 VALUES(?,'owner','project',?,'request','spec',?,'single','QUEUED',?,?,?,1)`, jobID, jobID, frozen, now, now, now+60000); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO goal_job_bindings(job_id,goal_id,plan_revision,graph_generation) VALUES(?,'g',?,?)`, jobID, d.NextPlanRevision, d.NextGraphGeneration); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO goal_plans(goal_id,revision,graph_generation,job_id,frozen_spec,graph_json,created)
 VALUES('g',?,?,?,?,'[]',?)`, d.NextPlanRevision, d.NextGraphGeneration, jobID, frozen, now)
	return err
}

func TestGuardReplanAllowsAndAdvancesGeneration(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	got, err := GuardReplan(context.Background(), db, request("r1", "e1", 1, 1, "jwt middleware conflicts", "oauth-adapter"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allowed || got.Code != DecisionAllowed || got.NextPlanRevision != 2 || got.NextGraphGeneration != 2 {
		t.Fatalf("unexpected decision: %+v", got)
	}
	var planRev, graph int64
	var replans int
	if err = db.SQL.QueryRow("SELECT active_plan_revision,active_graph_generation,consumed_replans FROM goals WHERE id='g'").Scan(&planRev, &graph, &replans); err != nil {
		t.Fatal(err)
	}
	if planRev != 2 || graph != 2 || replans != 1 {
		t.Fatalf("goal state plan=%d graph=%d replans=%d", planRev, graph, replans)
	}
}

func TestGuardPublicationConsumesApprovalAtomically(t *testing.T) {
	db := newGoalDB(t)
	ctx := context.Background()
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	if _, err := db.SQL.Exec("UPDATE goals SET state='NEEDS_APPROVAL' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}
	actor := governance.Actor{ID: "human-1", Kind: "human", Owner: "owner", Projects: []string{"project"}, Scopes: []string{"goals:approve"}}
	approval := governance.Request{OperationID: "approval-1", ExpectedVersion: 1, PlanRevision: 1, GraphGeneration: 1, Action: "APPROVE_NEXT_REPLAN", Reason: "reviewed evidence"}
	if err := db.Tx(ctx, func(q store.Query) error {
		_, err := governance.DecideTx(ctx, q, actor, "g", approval)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec("CREATE TABLE publication_fixture(revision INTEGER PRIMARY KEY, generation INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	in := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	publish := func(ctx context.Context, q store.Query, decision ReplanDecision) error {
		_, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO publication_fixture VALUES(?,?)", decision.NextPlanRevision, decision.NextGraphGeneration)
		if err != nil {
			return err
		}
		return publishTestPlan(ctx, q, decision)
	}
	if _, err := GuardAndPublishReplan(ctx, db, in, "", publish); err == nil || err.Error() != "INVALID_REPLAN_PERMISSION" {
		t.Fatalf("pending approval was bypassed: %v", err)
	}
	crash := errors.New("publish failed")
	if _, err := GuardAndPublishReplan(ctx, db, in, "approval-1", func(ctx context.Context, q store.Query, d ReplanDecision) error {
		if err := publish(ctx, q, d); err != nil {
			return err
		}
		return crash
	}); !errors.Is(err, crash) {
		t.Fatalf("publication failure: %v", err)
	}
	var requests, published, consumed int
	if err := db.SQL.QueryRow("SELECT count(*) FROM replan_requests WHERE goal_id='g'").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow("SELECT count(*) FROM publication_fixture").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow("SELECT consumed FROM goal_replan_permissions WHERE goal_id='g' AND operation_id='approval-1'").Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || published != 0 || consumed != 0 {
		t.Fatalf("failed publication left partial state: requests=%d published=%d consumed=%d", requests, published, consumed)
	}
	first, err := GuardAndPublishReplan(ctx, db, in, "approval-1", publish)
	if err != nil || !first.Allowed || first.NextPlanRevision != 2 {
		t.Fatalf("publication=%+v err=%v", first, err)
	}
	replayed, err := GuardAndPublishReplan(ctx, db, in, "approval-1", publish)
	if err != nil || !replayed.Existing || !replayed.Allowed {
		t.Fatalf("verified publication replay=%+v err=%v", replayed, err)
	}
	if err := db.SQL.QueryRow("SELECT count(*) FROM publication_fixture").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow("SELECT consumed FROM goal_replan_permissions WHERE goal_id='g' AND operation_id='approval-1'").Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if published != 1 || consumed != 1 {
		t.Fatalf("publication replay duplicated work: published=%d consumed=%d", published, consumed)
	}
}

func TestAutonomousGuardPublicationSharesTransaction(t *testing.T) {
	db := newGoalDB(t)
	ctx := context.Background()
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	in := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	if _, err := GuardAndPublishReplan(ctx, db, in, "", nil); err == nil {
		t.Fatal("missing publication callback accepted")
	}
	if _, err := db.SQL.Exec("CREATE TABLE publication_fixture(revision INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	decision, err := GuardAndPublishReplan(ctx, db, in, "", func(ctx context.Context, q store.Query, d ReplanDecision) error {
		if _, err := q.ExecContext(ctx, "INSERT INTO publication_fixture VALUES(?)", d.NextPlanRevision); err != nil {
			return err
		}
		return publishTestPlan(ctx, q, d)
	})
	if err != nil || !decision.Allowed || decision.NextPlanRevision != 2 {
		t.Fatalf("autonomous publication=%+v err=%v", decision, err)
	}
	var n int
	if err := db.SQL.QueryRow("SELECT count(*) FROM publication_fixture WHERE revision=2").Scan(&n); err != nil || n != 1 {
		t.Fatalf("publication missing: count=%d err=%v", n, err)
	}
}

func TestGuardPublicationRejectsEmptyCallbackAndRollsBack(t *testing.T) {
	db := newGoalDB(t)
	ctx := context.Background()
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	in := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	if _, err := GuardAndPublishReplan(ctx, db, in, "", func(context.Context, store.Query, ReplanDecision) error { return nil }); err == nil || err.Error() != "REPLAN_PUBLICATION_MISSING" {
		t.Fatalf("empty publication accepted: %v", err)
	}
	var revision int64
	var requests int
	if err := db.SQL.QueryRow("SELECT active_plan_revision FROM goals WHERE id='g'").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow("SELECT count(*) FROM replan_requests WHERE goal_id='g'").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || requests != 0 {
		t.Fatalf("empty publication left partial decision: revision=%d requests=%d", revision, requests)
	}
}

func TestGuardPublicationReplayRequiresCommittedPlanJobBinding(t *testing.T) {
	db := newGoalDB(t)
	ctx := context.Background()
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	in := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	called := 0
	publish := func(ctx context.Context, q store.Query, d ReplanDecision) error {
		called++
		return publishTestPlan(ctx, q, d)
	}
	if _, err := GuardAndPublishReplan(ctx, db, in, "", publish); err != nil {
		t.Fatal(err)
	}
	replayed, err := GuardAndPublishReplan(ctx, db, in, "", publish)
	if err != nil || !replayed.Existing || !replayed.Allowed || called != 1 {
		t.Fatalf("verified replay=%+v err=%v callback count=%d", replayed, err, called)
	}
	if _, err := db.SQL.Exec(`UPDATE jobs SET spec='{"spec":"changed"}' WHERE id='replan-job-2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := GuardAndPublishReplan(ctx, db, in, "", publish); err == nil || err.Error() != "REPLAN_PUBLICATION_REPLAY_UNVERIFIED" {
		t.Fatalf("mismatched frozen Job accepted: %v", err)
	}
	if _, err := db.SQL.Exec(`UPDATE jobs SET spec='{"spec":"frozen"}',owner='other' WHERE id='replan-job-2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := GuardAndPublishReplan(ctx, db, in, "", publish); err == nil || err.Error() != "REPLAN_PUBLICATION_REPLAY_UNVERIFIED" {
		t.Fatalf("cross-owner Job accepted: %v", err)
	}
}

func TestGuardReplanIsIdempotentPerEvaluation(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	req := request("r1", "e1", 1, 1, "jwt middleware conflicts", "oauth-adapter")
	first, err := GuardReplan(context.Background(), db, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ID = "different-delivery-id"
	second, err := GuardReplan(context.Background(), db, req)
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

func TestGuardReplanRejectsIdempotencyConflict(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	req := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	if _, err := GuardReplan(context.Background(), db, req); err != nil {
		t.Fatal(err)
	}
	req.ID = "r2"
	req.Evidence.Evidence[0].Fact = "fact-b"
	if _, err := GuardReplan(context.Background(), db, req); err == nil || err.Error() != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestGuardReplanRejectsStaleGeneration(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 3, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	if _, err := GuardReplan(context.Background(), db, request("r1", "e1", 1, 1, "fact-a", "strategy-a")); err != nil {
		t.Fatal(err)
	}
	got, err := GuardReplan(context.Background(), db, request("r2", "late-eval", 1, 1, "fact-b", "strategy-b"))
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
	first, err := GuardReplan(context.Background(), db, request("r1", "e1", 1, 1, "fact-a", "strategy-a"))
	if err != nil || !first.Allowed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err = db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}
	second, err := GuardReplan(context.Background(), db, request("r2", "e2", 2, 2, "fact-b", "strategy-b"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Allowed || !second.NeedsApproval || second.Code != DecisionBudgetExhausted {
		t.Fatalf("budget guard failed: %+v", second)
	}
}

func TestGuardReplanRequiresNewEvidence(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 3, MaxTotalAttempts: 5, MaxWallTime: time.Hour})
	if _, err := GuardReplan(context.Background(), db, request("r1", "e1", 1, 1, "same-fact", "strategy-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}
	got, err := GuardReplan(context.Background(), db, request("r2", "e2", 2, 2, "same-fact", "strategy-b"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionNoNewEvidence {
		t.Fatalf("duplicate evidence accepted: %+v", got)
	}
}

func TestGuardReplanRejectsDuplicatePlan(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 3, MaxTotalAttempts: 5, MaxWallTime: time.Hour})
	if _, err := GuardReplan(context.Background(), db, request("r1", "e1", 1, 1, "fact-a", "strategy-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}
	got, err := GuardReplan(context.Background(), db, request("r2", "e2", 2, 2, "fact-b", "strategy-a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionDuplicatePlan {
		t.Fatalf("duplicate plan accepted: %+v", got)
	}
}

func TestGuardReplanRejectsInitialPlanFingerprint(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 2, MaxTotalAttempts: 4, MaxWallTime: time.Hour})
	got, err := GuardReplan(context.Background(), db, request("r1", "e1", 1, 1, "fact-a", "initial"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionDuplicatePlan {
		t.Fatalf("initial plan reused: %+v", got)
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
	got, err := GuardReplan(context.Background(), db, request("r1", "e1", 1, 1, "fact-a", "strategy-a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionDeadlineExceeded {
		t.Fatalf("deadline guard failed: %+v", got)
	}
}

func TestEvidenceFingerprintOrderIndependent(t *testing.T) {
	a := ReplanEvidence{FailureClass: FailureTest, Evidence: []Evidence{
		{Type: "test", ArtifactID: "b", Fact: "B"},
		{Type: "test", ArtifactID: "a", Fact: "A"},
	}}
	b := ReplanEvidence{FailureClass: FailureTest, Evidence: []Evidence{
		{Type: "test", ArtifactID: "a", Fact: "A"},
		{Type: "test", ArtifactID: "b", Fact: "B"},
	}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("evidence fingerprint depends on input order")
	}
}

func TestPlanFingerprintCanonicalizesSetFields(t *testing.T) {
	a := plan("same")
	b := plan("same")
	b.RequiredCapabilities = []string{"tool:git", "runtime:codex"}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("plan fingerprint depends on capability order")
	}
}

func TestGuardReplanDetectsStrategyCycle(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 4, MaxTotalAttempts: 8, MaxWallTime: time.Hour})

	first := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	first.ProposedPlan.StrategyClass = "oauth-adapter"
	first.Progress.AcceptedChecks = 2
	if got, err := GuardReplan(context.Background(), db, first); err != nil || !got.Allowed {
		t.Fatalf("first=%+v err=%v", got, err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}

	second := request("r2", "e2", 2, 2, "fact-b", "strategy-b")
	second.ProposedPlan.StrategyClass = "oauth-adapter"
	second.ProposedPlan.Strategy = "rewrite oauth adapter with different wording"
	second.Progress.AcceptedChecks = 3
	got, err := GuardReplan(context.Background(), db, second)
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionLoopDetected {
		t.Fatalf("strategy cycle accepted: %+v", got)
	}
}

func TestGuardReplanStopsThirdSameFailureClass(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 5, MaxTotalAttempts: 10, MaxWallTime: time.Hour})

	r1 := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	r1.Progress.AcceptedChecks = 2
	if got, err := GuardReplan(context.Background(), db, r1); err != nil || !got.Allowed {
		t.Fatalf("r1=%+v err=%v", got, err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}

	r2 := request("r2", "e2", 2, 2, "fact-b", "strategy-b")
	r2.Progress.AcceptedChecks = 3
	if got, err := GuardReplan(context.Background(), db, r2); err != nil || !got.Allowed {
		t.Fatalf("r2=%+v err=%v", got, err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}

	r3 := request("r3", "e3", 3, 3, "fact-c", "strategy-c")
	r3.Progress.AcceptedChecks = 4
	got, err := GuardReplan(context.Background(), db, r3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionRepeatedFailure {
		t.Fatalf("third same failure accepted: %+v", got)
	}
}

func TestGuardReplanAllowsOneStagnantRoundThenStops(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 5, MaxTotalAttempts: 10, MaxWallTime: time.Hour})

	r1 := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	r1.Evidence.FailureClass = FailureTest
	r1.Progress = ProgressSnapshot{AcceptedChecks: 2, FailedChecks: 2, UnknownChecks: 1, ResolvedAssumptions: 1, UnresolvedBlockers: 2}
	if got, err := GuardReplan(context.Background(), db, r1); err != nil || !got.Allowed {
		t.Fatalf("r1=%+v err=%v", got, err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}

	r2 := request("r2", "e2", 2, 2, "fact-b", "strategy-b")
	r2.Evidence.FailureClass = FailureTimeout
	r2.Progress = r1.Progress
	if got, err := GuardReplan(context.Background(), db, r2); err != nil || !got.Allowed {
		t.Fatalf("first stagnant round should be allowed: got=%+v err=%v", got, err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}

	r3 := request("r3", "e3", 3, 3, "fact-c", "strategy-c")
	r3.Evidence.FailureClass = FailureDependency
	r3.Progress = r2.Progress
	got, err := GuardReplan(context.Background(), db, r3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || !got.NeedsApproval || got.Code != DecisionNoProgress {
		t.Fatalf("second stagnant round accepted: %+v", got)
	}
}

func TestGuardReplanProgressResetsStagnation(t *testing.T) {
	db := newGoalDB(t)
	createTestGoal(t, db, "g", Budget{MaxReplans: 5, MaxTotalAttempts: 10, MaxWallTime: time.Hour})

	r1 := request("r1", "e1", 1, 1, "fact-a", "strategy-a")
	r1.Evidence.FailureClass = FailureTest
	r1.Progress = ProgressSnapshot{AcceptedChecks: 1, FailedChecks: 2, UnknownChecks: 2, ResolvedAssumptions: 0, UnresolvedBlockers: 2}
	if got, err := GuardReplan(context.Background(), db, r1); err != nil || !got.Allowed {
		t.Fatalf("r1=%+v err=%v", got, err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}

	r2 := request("r2", "e2", 2, 2, "fact-b", "strategy-b")
	r2.Evidence.FailureClass = FailureTimeout
	r2.Progress = r1.Progress
	if got, err := GuardReplan(context.Background(), db, r2); err != nil || !got.Allowed {
		t.Fatalf("r2=%+v err=%v", got, err)
	}
	if _, err := db.SQL.Exec("UPDATE goals SET state='EVALUATING' WHERE id='g'"); err != nil {
		t.Fatal(err)
	}

	r3 := request("r3", "e3", 3, 3, "fact-c", "strategy-c")
	r3.Evidence.FailureClass = FailureDependency
	r3.Progress = r2.Progress
	r3.Progress.AcceptedChecks = 2
	got, err := GuardReplan(context.Background(), db, r3)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allowed {
		t.Fatalf("real progress should reset stagnation: %+v", got)
	}
}
