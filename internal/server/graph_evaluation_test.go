package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestGraphGoalRequiresAuthorizedBusinessEvaluation(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate")
	})
	j, e := h.s.SubmitGraph(h.ctx, "eval", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	var hash string
	if e = h.s.db.SQL.QueryRow(`SELECT output_hash FROM iu_graphs WHERE job_id=?`, j.ID).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	unknown := GraphEvaluation{ID: "unknown", OutputHash: hash, Verifier: "external", VerifierVersion: "1", Verdict: "UNKNOWN", EvidenceHash: hash}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, unknown); e != nil {
		t.Fatal(e)
	}
	status, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil || status.GoalState != "EVALUATING" || status.EvaluationVersion != 1 {
		t.Fatalf("unknown %+v %v", status, e)
	}
	pass := GraphEvaluation{ID: "pass", ExpectedVersion: 1, Supersedes: status.EvaluationID, OutputHash: hash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: hash}
	wrong := pass
	wrong.OutputHash = "wrong"
	if e = h.s.EvaluateGraph(h.ctx, j.ID, wrong); e == nil {
		t.Fatal("wrong output accepted")
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, pass); e != nil {
		t.Fatal(e)
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, pass); e != nil {
		t.Fatalf("idempotent replay %v", e)
	}
	status, e = h.s.GraphStatus(h.ctx, j.ID)
	if e != nil || status.GoalState != "SUCCEEDED" {
		t.Fatalf("goal %+v %v", status, e)
	}
}

func TestGraphBudgetedGoalProjectionAndWaitingDeadline(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Jobs.Templates = append(c.Jobs.Templates, config.JobTemplate{RuntimeProfile: "claude_http", PolicyRef: "review", AcceptanceProfile: "check", Digest: strings.Repeat("a", 64)})
	})
	spec := graphSpec(h)
	cost := int64(100)
	spec.Limits.MaxCostUnits = &cost
	spec.Operators[0].Execution.RuntimeProfile = "claude_http"
	spec.Operators[0].Execution.Engine = "claude"
	j, e := h.s.SubmitGraph(h.ctx, "budget-projection", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	supported, _, e := projectedRuntimeBudgetSupport(h.ctx, h.s.db.SQL, j.graph.GoalID)
	if e != nil || !supported {
		t.Fatalf("budget projection %v %v", supported, e)
	}
	for _, state := range []string{"EVALUATING", "REPLAN_GUARDING", "NEEDS_APPROVAL"} {
		if _, e = h.s.db.SQL.Exec(`UPDATE goals SET state=?,deadline=1 WHERE id=?`, state, j.graph.GoalID); e != nil {
			t.Fatal(e)
		}
		if e = h.s.reconcileGraphEvaluations(h.ctx); e != nil {
			t.Fatal(e)
		}
		var got string
		if e = h.s.db.SQL.QueryRow(`SELECT state FROM goals WHERE id=?`, j.graph.GoalID).Scan(&got); e != nil || got != "FAILED" {
			t.Fatalf("expired %s -> %s %v", state, got, e)
		}
	}
}

func TestGraphBusinessFailureCanPublishGuardedNewGeneration(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate", "goals:propose")
	})
	spec := graphSpec(h)
	spec.Limits.MaxReplans = 1
	j, e := h.s.SubmitGraph(h.ctx, "repair", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	var hash string
	if e = h.s.db.SQL.QueryRow(`SELECT output_hash FROM iu_graphs WHERE job_id=?`, j.ID).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	ev := GraphEvaluation{ID: "failed", OutputHash: hash, Verifier: "external", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: hash}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	current, e := readJob(h.ctx, h.s.db.SQL, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.db.Tx(h.ctx, func(q store.Query) error { return evaluateBoundJob(h.ctx, q, current) }); e != nil {
		t.Fatal(e)
	}
	var goalState string
	if e = h.s.db.SQL.QueryRow(`SELECT state FROM goals WHERE id=?`, st.GoalID).Scan(&goalState); e != nil || goalState != "REPLAN_GUARDING" {
		t.Fatalf("projection reset business evaluation: %s %v", goalState, e)
	}
	spec.Nodes[0].Instructions = "correct the global discount"
	plan, e := graph.Compile(spec)
	if e != nil {
		t.Fatal(e)
	}
	in := replanProposal(st.GoalID, st.EvaluationID, h.spec("single"))
	in.ProposedPlan.JobSpecHash = plan.Digest
	in.Evidence = goal.ReplanEvidence{FailureClass: goal.FailureTest, Evidence: []goal.Evidence{{Type: "iu_graph_evaluation", Fact: graphEvaluationFact(ev)}}}
	next, decision, e := h.s.PublishGoalReplan(h.ctx, j.ID, in, "", job.JSON(spec))
	if e != nil || !decision.Allowed || next == nil {
		b, _ := json.Marshal(decision)
		t.Fatalf("publish %s %v", b, e)
	}
	if next.graph.Generation != 2 || next.graph.GoalID != st.GoalID {
		t.Fatal("wrong graph namespace")
	}
	replay, decision, e := h.s.PublishGoalReplan(h.ctx, j.ID, in, "", job.JSON(spec))
	if e != nil || !decision.Existing || replay.ID != next.ID {
		t.Fatalf("replay %v %+v", e, decision)
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, GraphEvaluation{ID: "late", ExpectedVersion: 1, Supersedes: st.EvaluationID, OutputHash: hash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: hash}); e == nil {
		t.Fatal("late old-generation pass accepted")
	}
	seedGraphCandidate(t, h, next, graphCandidateOutput(t, h, next))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, next.ID); e != nil {
		t.Fatal(e)
	}
	st, e = h.s.GraphStatus(h.ctx, next.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.EvaluateGraph(h.ctx, next.ID, GraphEvaluation{ID: "failed", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: st.OutputHash}); e != nil {
		t.Fatalf("same key across generations: %v", e)
	}
	st, e = h.s.GraphStatus(h.ctx, next.ID)
	if e != nil || st.GoalState != "FAILED" {
		t.Fatalf("exhausted replans still live %+v %v", st, e)
	}
}

func TestGraphCancelWaitingEvaluationPreventsLatePass(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate")
	})
	j, e := h.s.SubmitGraph(h.ctx, "cancel-evaluation", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = h.s.CancelJob(h.ctx, j.ID, CancelJobRequest{ControlID: "cancel", Reason: "stop goal"}); e != nil {
		t.Fatal(e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil || st.GoalState != "CANCELED" {
		t.Fatalf("cancel did not stop Goal %+v %v", st, e)
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, GraphEvaluation{ID: "late", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: st.OutputHash}); e == nil {
		t.Fatal("late pass reopened canceled Goal")
	}
}

func TestGraphEvaluationRequiresSeparateAuthority(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	j, e := h.s.SubmitGraph(h.ctx, "auth", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, GraphEvaluation{ID: "self-pass", Verdict: "PASS"}); e == nil {
		t.Fatal("submitter self-evaluated without scope")
	}
}

func TestGraphEvaluationRejectsExpiredGoalBeforeReconciler(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate")
	})
	j, e := h.s.SubmitGraph(h.ctx, "expired-evaluation", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.s.db.SQL.Exec("UPDATE goals SET deadline=? WHERE id=?", store.Now()-1000, st.GoalID); e != nil {
		t.Fatal(e)
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, GraphEvaluation{ID: "too-late", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: st.OutputHash}); e == nil {
		t.Fatal("expired Goal accepted PASS before deadline reconciler")
	}
}
