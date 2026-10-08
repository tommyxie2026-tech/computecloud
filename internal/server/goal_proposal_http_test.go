package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func proposalHTTPBody(p goal.ReplanRequest, spec job.Spec) []byte {
	return job.JSON(map[string]any{
		"proposal": map[string]any{
			"id": p.ID, "goal_id": p.GoalID, "evaluation_id": p.EvaluationID,
			"expected_plan_revision": p.ExpectedPlanRevision, "expected_graph_generation": p.ExpectedGraphGeneration,
			"reason_code": p.ReasonCode,
			"evidence": map[string]any{"failure_class": p.Evidence.FailureClass,
				"evidence": []map[string]any{{"type": p.Evidence.Evidence[0].Type, "artifact_id": p.Evidence.Evidence[0].ArtifactID, "fact": p.Evidence.Evidence[0].Fact}}},
			"proposed_plan": map[string]any{
				"strategy": p.ProposedPlan.Strategy, "strategy_class": p.ProposedPlan.StrategyClass,
				"dependency_signature":  p.ProposedPlan.DependencySignature,
				"required_capabilities": p.ProposedPlan.RequiredCapabilities, "key_assumptions": p.ProposedPlan.KeyAssumptions,
				"evaluation_strategy": p.ProposedPlan.EvaluationStrategy, "side_effect_class": p.ProposedPlan.SideEffectClass,
				"job_spec_hash": p.ProposedPlan.JobSpecHash,
			},
			"strategy_delta": map[string]any{"changed_dimensions": p.StrategyDelta.ChangedDimensions, "summary": p.StrategyDelta.Summary},
			"progress": map[string]any{"accepted_checks": p.Progress.AcceptedChecks, "failed_checks": p.Progress.FailedChecks,
				"unknown_checks": p.Progress.UnknownChecks, "resolved_assumptions": p.Progress.ResolvedAssumptions,
				"unresolved_blockers": p.Progress.UnresolvedBlockers},
		},
		"job_spec": spec,
	})
}

func TestGoalHTTPProposalLeaseAndReplay(t *testing.T) {
	s, _, _, _ := offlineJobServer(t)
	cfg := s.cfg
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Users[0].Scopes = append(cfg.Users[0].Scopes, "goals:propose")
	var err error
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	token, err := config.Token(cfg.Users[0].TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.auth.Bearer(context.Background(), "Bearer "+token)
	if err != nil {
		t.Fatal(err)
	}
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	prior, err := s.SubmitJob(ctx, "goal-http-original", job.JSON(spec))
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
	lease, err := s.acquireControlWriteLease(ctx, prior.ID, "goal-proposal-test")
	if err != nil {
		t.Fatal(err)
	}
	body := proposalHTTPBody(proposal, spec)
	call := func(leaseToken string, payload []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+prior.ID+"/goal/replans", bytes.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if leaseToken != "" {
			r.Header.Set("X-Control-Lease", leaseToken)
		}
		w := httptest.NewRecorder()
		s.HTTPHandler().ServeHTTP(w, r)
		return w
	}
	if w := call("", body); w.Code != http.StatusConflict {
		t.Fatalf("missing lease=%d %s", w.Code, w.Body.String())
	}
	if w := call(lease.LeaseToken, body); w.Code != http.StatusAccepted {
		t.Fatalf("publication=%d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := call(lease.LeaseToken, body)
	if w.Code != http.StatusOK {
		t.Fatalf("replay=%d %s", w.Code, w.Body.String())
	}
	var response struct {
		Decision map[string]any `json:"decision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Decision["existing"] != true {
		t.Fatalf("replay decision=%v", response.Decision)
	}
	changed := spec
	changed.Input.Text = "different"
	if w := call(lease.LeaseToken, proposalHTTPBody(proposal, changed)); w.Code != http.StatusBadRequest {
		t.Fatalf("changed spec=%d %s", w.Code, w.Body.String())
	}
	conflict := proposal
	conflict.ProposedPlan.Strategy = "different strategy"
	if w := call(lease.LeaseToken, proposalHTTPBody(conflict, spec)); w.Code != http.StatusConflict {
		t.Fatalf("conflicting replay=%d %s", w.Code, w.Body.String())
	}
	changedID := proposal
	changedID.ID = "changed-request-id"
	if w := call(lease.LeaseToken, proposalHTTPBody(changedID, spec)); w.Code != http.StatusConflict {
		t.Fatalf("changed request id replay=%d %s", w.Code, w.Body.String())
	}
	invalid := proposal
	invalid.StrategyDelta.Summary = ""
	if w := call(lease.LeaseToken, proposalHTTPBody(invalid, spec)); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid proposal=%d %s", w.Code, w.Body.String())
	}
	stale := proposal
	stale.ExpectedGraphGeneration = 2
	if w := call(lease.LeaseToken, proposalHTTPBody(stale, spec)); w.Code == http.StatusAccepted || w.Code == http.StatusOK {
		t.Fatalf("stale generation accepted=%d %s", w.Code, w.Body.String())
	}
	if w := call(lease.LeaseToken, append(body, []byte(` {}`)...)); w.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON accepted=%d %s", w.Code, w.Body.String())
	}
	var plans int
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM goal_plans WHERE goal_id=?", goalID).Scan(&plans); err != nil || plans != 2 {
		t.Fatalf("plans=%d err=%v", plans, err)
	}
}
