package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/governance"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func goalProposerContext(ctx context.Context) context.Context {
	p, _ := rpcutil.PrincipalFrom(ctx)
	p.Identity.Scopes = append(p.Identity.Scopes, "goals:propose")
	return rpcutil.WithPrincipal(ctx, p)
}

func TestServerGoalReplanRequiresProposerScope(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer s.Close()
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	prior, err := s.SubmitJob(ctx, "goal-scope-original", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	goalID := "goal_" + prior.ID
	if _, err := s.db.SQL.Exec("UPDATE goals SET max_replans=1,max_total_attempts=2 WHERE id=?", goalID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Tx(ctx, func(q store.Query) error { return jobState(ctx, q, prior, "FAILED", "", "TEST_FAILURE") }); err != nil {
		t.Fatal(err)
	}
	proposal := replanProposal(goalID, fmt.Sprintf("%s:%d", prior.ID, prior.Version), spec)
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(spec)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary submitter published Re-plan: %v", err)
	}
}

func replanProposal(goalID, evaluationID string, spec job.Spec) goal.ReplanRequest {
	return goal.ReplanRequest{
		ID: "proposal-1", GoalID: goalID, EvaluationID: evaluationID,
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
		ReasonCode: "NEW_EVIDENCE",
		Evidence:   goal.ReplanEvidence{FailureClass: goal.FailureTest, Evidence: []goal.Evidence{{Type: "job_evaluation", Fact: "TEST_FAILURE"}}},
		ProposedPlan: goal.PlanCanonical{
			Strategy: "retry-with-fix", StrategyClass: "bounded-retry", DependencySignature: "single",
			RequiredCapabilities: []string{"runtime:codex_exec"}, KeyAssumptions: []string{"fix available"},
			EvaluationStrategy: "job-artifact-v1", SideEffectClass: "workspace",
			JobSpecHash: job.Hash(job.JSON(spec)),
		},
		StrategyDelta: goal.StrategyDelta{ChangedDimensions: []string{"strategy"}, Summary: "apply evidence"},
		Progress:      goal.ProgressSnapshot{AcceptedChecks: 1, FailedChecks: 1, UnknownChecks: 1, ResolvedAssumptions: 1, UnresolvedBlockers: 1},
	}
}

func TestServerGoalReplanPublicationAndReplay(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer s.Close()
	ctx = goalProposerContext(ctx)
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	prior, err := s.SubmitJob(ctx, "goal-replan-original", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	goalID := "goal_" + prior.ID
	if _, err := s.db.SQL.Exec("UPDATE goals SET max_replans=1,max_total_attempts=2 WHERE id=?", goalID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Tx(ctx, func(q store.Query) error { return jobState(ctx, q, prior, "FAILED", "", "TEST_FAILURE") }); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.db.SQL.QueryRow("SELECT state FROM goals WHERE id=?", goalID).Scan(&state); err != nil || state != "REPLAN_GUARDING" {
		t.Fatalf("failed Job did not enter replan guarding: state=%s err=%v", state, err)
	}
	proposal := replanProposal(goalID, fmt.Sprintf("%s:%d", prior.ID, prior.Version), spec)
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(spec)); err != nil {
		t.Fatal(err)
	}
	created, decision, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(spec))
	if err != nil || created == nil || !created.Existing || !decision.Existing || !decision.Allowed {
		t.Fatalf("replay job=%+v decision=%+v err=%v", created, decision, err)
	}
	var jobs, plans, tasks int
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM goal_job_bindings WHERE goal_id=?", goalID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM goal_plans WHERE goal_id=?", goalID).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=?", created.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || plans != 2 || tasks != 1 || decision.NextPlanRevision != 2 || decision.NextGraphGeneration != 2 {
		t.Fatalf("publication jobs=%d plans=%d tasks=%d decision=%+v", jobs, plans, tasks, decision)
	}
}

func TestServerGoalReplanPublishesClaudeHTTPCostBudget(t *testing.T) {
	s, ctx, _, peer := offlineJobServer(t)
	defer s.Close()
	ctx = goalProposerContext(ctx)
	priorSpec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	prior, err := s.SubmitJob(ctx, "goal-cost-budget-original", job.JSON(priorSpec))
	if err != nil {
		t.Fatal(err)
	}
	goalID := "goal_" + prior.ID
	if _, err = s.db.SQL.Exec("UPDATE goals SET max_replans=1,max_total_attempts=2 WHERE id=?", goalID); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Tx(ctx, func(q store.Query) error { return jobState(ctx, q, prior, "FAILED", "", "TEST_FAILURE") }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,?)", goalID, 2_000_000); err != nil {
		t.Fatal(err)
	}
	claudeSpec := configureClaudeHTTPBudgetFixture(s, peer)
	proposal := replanProposal(goalID, fmt.Sprintf("%s:%d", prior.ID, prior.Version), claudeSpec)
	proposal.ID = "proposal-claude-http-cost"
	created, decision, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(claudeSpec))
	if err != nil || created == nil || !decision.Allowed {
		t.Fatalf("cost-only claude_http replan rejected: job=%+v decision=%+v err=%v", created, decision, err)
	}
}

func TestServerGoalReplanRejectsMismatchAndRollsBack(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer s.Close()
	ctx = goalProposerContext(ctx)
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	prior, err := s.SubmitJob(ctx, "goal-replan-original", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	goalID := "goal_" + prior.ID
	if _, err := s.db.SQL.Exec("UPDATE goals SET max_replans=1,max_total_attempts=2 WHERE id=?", goalID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Tx(ctx, func(q store.Query) error { return jobState(ctx, q, prior, "FAILED", "", "TEST_FAILURE") }); err != nil {
		t.Fatal(err)
	}
	proposal := replanProposal(goalID, fmt.Sprintf("%s:%d", prior.ID, prior.Version), spec)
	forged := proposal
	forged.EvaluationID = "forged-evaluation"
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, forged, "", job.JSON(spec)); err == nil {
		t.Fatal("unverified evaluation accepted")
	}
	forged = proposal
	forged.Evidence.Evidence = []goal.Evidence{{Type: "job_evaluation", Fact: "fabricated failure"}}
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, forged, "", job.JSON(spec)); err == nil {
		t.Fatal("fabricated evaluation evidence accepted")
	}
	forged = proposal
	forged.Evidence.Evidence = []goal.Evidence{{Type: "job_evaluation", ArtifactID: "unverified-artifact", Fact: "TEST_FAILURE"}}
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, forged, "", job.JSON(spec)); err == nil {
		t.Fatal("unverified artifact reference accepted")
	}
	badSpec := spec
	badSpec.Input.Text = "different input"
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(badSpec)); err == nil {
		t.Fatal("mismatched proposed Job accepted")
	}
	s.cfg.Credentials["account"] = 0
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(spec)); err == nil {
		t.Fatal("unavailable credential accepted")
	}
	var revision int64
	var requests int
	if err := s.db.SQL.QueryRow("SELECT active_plan_revision FROM goals WHERE id=?", goalID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM replan_requests WHERE goal_id=?", goalID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || requests != 0 {
		t.Fatalf("failed publication left state revision=%d requests=%d", revision, requests)
	}
	s.cfg.Credentials["account"] = 1
	if _, err := s.db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_tokens) VALUES(?,100)", goalID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(spec)); err == nil {
		t.Fatal("unsupported finite Runtime budget accepted")
	}
	if _, err := s.db.SQL.Exec("DELETE FROM goal_budget_policy WHERE goal_id=?", goalID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(spec)); err != nil {
		t.Fatalf("retry after rollback failed: %v", err)
	}
}

func TestServerGoalReplanPublishesExistingMapGraph(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer s.Close()
	ctx = goalProposerContext(ctx)
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("report_merge_v1")
	prior, err := s.SubmitJob(ctx, "goal-map-original", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	goalID := "goal_" + prior.ID
	if _, err := s.db.SQL.Exec("UPDATE goals SET max_replans=1,max_total_attempts=8 WHERE id=?", goalID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Tx(ctx, func(q store.Query) error { return jobState(ctx, q, prior, "FAILED", "", "TEST_FAILURE") }); err != nil {
		t.Fatal(err)
	}
	proposal := replanProposal(goalID, fmt.Sprintf("%s:%d", prior.ID, prior.Version), spec)
	created, decision, err := s.PublishGoalReplan(ctx, prior.ID, proposal, "", job.JSON(spec))
	if err != nil || !decision.Allowed {
		t.Fatalf("map publication=%+v err=%v", decision, err)
	}
	var stages, tasks, graphNodes int
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM stages WHERE job_id=?", created.ID).Scan(&stages); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=?", created.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT json_array_length(graph_json) FROM goal_plans WHERE goal_id=? AND revision=2", goalID).Scan(&graphNodes); err != nil {
		t.Fatal(err)
	}
	if stages != 2 || tasks != len(spec.Map.Partitions) || graphNodes != len(spec.Map.Partitions)+1 {
		t.Fatalf("map graph stages=%d tasks=%d nodes=%d", stages, tasks, graphNodes)
	}
}

func TestServerGoalPendingProposalPublishesAfterHumanApproval(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer s.Close()
	ctx = goalProposerContext(ctx)
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	prior, err := s.SubmitJob(ctx, "goal-approval-original", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	goalID := "goal_" + prior.ID
	if _, err := s.db.SQL.Exec("UPDATE goals SET max_replans=3,max_total_attempts=5 WHERE id=?", goalID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Tx(ctx, func(q store.Query) error { return jobState(ctx, q, prior, "FAILED", "", "TEST_FAILURE") }); err != nil {
		t.Fatal(err)
	}
	first := replanProposal(goalID, fmt.Sprintf("%s:%d", prior.ID, prior.Version), spec)
	created, decision, err := s.PublishGoalReplan(ctx, prior.ID, first, "", job.JSON(spec))
	if err != nil || !decision.Allowed {
		t.Fatalf("first=%+v %v", decision, err)
	}
	if err := s.db.Tx(ctx, func(q store.Query) error { return jobState(ctx, q, created, "FAILED", "", "TIMEOUT") }); err != nil {
		t.Fatal(err)
	}
	second := replanProposal(goalID, fmt.Sprintf("%s:%d", created.ID, created.Version), spec)
	second.ID = "proposal-2"
	second.ExpectedPlanRevision = 2
	second.ExpectedGraphGeneration = 2
	second.Evidence.FailureClass = goal.FailureTimeout
	second.Evidence.Evidence[0].Fact = "TIMEOUT"
	second.ProposedPlan.Strategy = "retry-after-timeout"
	if _, pending, err := s.PublishGoalReplan(ctx, created.ID, second, "", job.JSON(spec)); err != nil || !pending.NeedsApproval || pending.Code != goal.DecisionLoopDetected {
		t.Fatalf("pending=%+v %v", pending, err)
	}
	var version int64
	if err := s.db.SQL.QueryRow("SELECT version FROM goals WHERE id=?", goalID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	approval := governance.Request{OperationID: "approval-1", ExpectedVersion: version, PlanRevision: 2, GraphGeneration: 2, Action: "APPROVE_NEXT_REPLAN", Reason: "reviewed strategy retry"}
	if _, err := s.DecideGoal(humanGoalContext(ctx), created.ID, approval); err != nil {
		t.Fatal(err)
	}
	approved, decision, err := s.PublishGoalReplan(ctx, created.ID, second, "approval-1", job.JSON(spec))
	if err != nil || !decision.Allowed || approved == nil {
		t.Fatalf("approved=%+v decision=%+v err=%v", approved, decision, err)
	}
	if replayed, replay, err := s.PublishGoalReplan(ctx, created.ID, second, "", job.JSON(spec)); err != nil || !replay.Existing || replayed.ID != approved.ID {
		t.Fatalf("replay=%+v decision=%+v err=%v", replayed, replay, err)
	}
}
