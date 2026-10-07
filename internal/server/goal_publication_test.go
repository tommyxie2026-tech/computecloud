package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func replanProposal(goalID, evaluationID string, spec job.Spec) goal.ReplanRequest {
	return goal.ReplanRequest{
		ID: "proposal-1", GoalID: goalID, EvaluationID: evaluationID,
		ExpectedPlanRevision: 1, ExpectedGraphGeneration: 1,
		ReasonCode: "NEW_EVIDENCE",
		Evidence:   goal.ReplanEvidence{FailureClass: goal.FailureInvalidAssumption, Evidence: []goal.Evidence{{Type: "test_failure", ArtifactID: "artifact-1", Fact: "new evidence"}}},
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

func TestServerGoalReplanRejectsMismatchAndRollsBack(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer s.Close()
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
