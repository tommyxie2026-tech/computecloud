package server

import (
	"encoding/json"
	"net/http"

	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The HTTP shape is separate from Goal's canonical structs: changing their
// JSON field names would change persisted fingerprints and replay identity.
type goalProposalBody struct {
	Proposal struct {
		ID                      string `json:"id"`
		GoalID                  string `json:"goal_id"`
		EvaluationID            string `json:"evaluation_id"`
		ExpectedPlanRevision    int64  `json:"expected_plan_revision"`
		ExpectedGraphGeneration int64  `json:"expected_graph_generation"`
		ReasonCode              string `json:"reason_code"`
		Evidence                struct {
			FailureClass goal.FailureClass `json:"failure_class"`
			Evidence     []struct {
				Type       string `json:"type"`
				ArtifactID string `json:"artifact_id"`
				Fact       string `json:"fact"`
			} `json:"evidence"`
		} `json:"evidence"`
		ProposedPlan struct {
			Strategy             string   `json:"strategy"`
			StrategyClass        string   `json:"strategy_class"`
			DependencySignature  string   `json:"dependency_signature"`
			RequiredCapabilities []string `json:"required_capabilities"`
			KeyAssumptions       []string `json:"key_assumptions"`
			EvaluationStrategy   string   `json:"evaluation_strategy"`
			SideEffectClass      string   `json:"side_effect_class"`
			JobSpecHash          string   `json:"job_spec_hash"`
		} `json:"proposed_plan"`
		StrategyDelta struct {
			ChangedDimensions []string `json:"changed_dimensions"`
			Summary           string   `json:"summary"`
		} `json:"strategy_delta"`
		Progress struct {
			AcceptedChecks      int `json:"accepted_checks"`
			FailedChecks        int `json:"failed_checks"`
			UnknownChecks       int `json:"unknown_checks"`
			ResolvedAssumptions int `json:"resolved_assumptions"`
			UnresolvedBlockers  int `json:"unresolved_blockers"`
		} `json:"progress"`
	} `json:"proposal"`
	ApprovalOperationID string          `json:"approval_operation_id"`
	JobSpec             json.RawMessage `json:"job_spec"`
}

type goalProposalDecision struct {
	Allowed             bool   `json:"allowed"`
	NeedsApproval       bool   `json:"needs_approval"`
	Code                string `json:"code"`
	NextPlanRevision    int64  `json:"next_plan_revision"`
	NextGraphGeneration int64  `json:"next_graph_generation"`
	Existing            bool   `json:"existing"`
}

func goalProposalDecisionJSON(d goal.ReplanDecision) goalProposalDecision {
	return goalProposalDecision{d.Allowed, d.NeedsApproval, d.Code, d.NextPlanRevision, d.NextGraphGeneration, d.Existing}
}

func (b goalProposalBody) request() goal.ReplanRequest {
	p := b.Proposal
	evidence := make([]goal.Evidence, 0, len(p.Evidence.Evidence))
	for _, e := range p.Evidence.Evidence {
		evidence = append(evidence, goal.Evidence{Type: e.Type, ArtifactID: e.ArtifactID, Fact: e.Fact})
	}
	return goal.ReplanRequest{
		ID: p.ID, GoalID: p.GoalID, EvaluationID: p.EvaluationID,
		ExpectedPlanRevision: p.ExpectedPlanRevision, ExpectedGraphGeneration: p.ExpectedGraphGeneration,
		ReasonCode: p.ReasonCode,
		Evidence:   goal.ReplanEvidence{FailureClass: p.Evidence.FailureClass, Evidence: evidence},
		ProposedPlan: goal.PlanCanonical{
			Strategy: p.ProposedPlan.Strategy, StrategyClass: p.ProposedPlan.StrategyClass,
			DependencySignature:  p.ProposedPlan.DependencySignature,
			RequiredCapabilities: p.ProposedPlan.RequiredCapabilities, KeyAssumptions: p.ProposedPlan.KeyAssumptions,
			EvaluationStrategy: p.ProposedPlan.EvaluationStrategy, SideEffectClass: p.ProposedPlan.SideEffectClass,
			JobSpecHash: p.ProposedPlan.JobSpecHash,
		},
		StrategyDelta: goal.StrategyDelta{ChangedDimensions: p.StrategyDelta.ChangedDimensions, Summary: p.StrategyDelta.Summary},
		Progress: goal.ProgressSnapshot{
			AcceptedChecks: p.Progress.AcceptedChecks, FailedChecks: p.Progress.FailedChecks,
			UnknownChecks: p.Progress.UnknownChecks, ResolvedAssumptions: p.Progress.ResolvedAssumptions,
			UnresolvedBlockers: p.Progress.UnresolvedBlockers,
		},
	}
}

func (s *Server) httpGoalProposal(w http.ResponseWriter, r *http.Request) {
	if err := s.requireControlWriteLease(r, r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	raw, err := readJSONBody(w, r, int64(s.cfg.Jobs.MaxRequestBytes)+16*1024)
	if err != nil {
		httpError(w, err)
		return
	}
	var in goalProposalBody
	if err := jsonutil.Decode(raw, &in); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "INVALID_GOAL_PROPOSAL"))
		return
	}
	j, decision, err := s.PublishGoalReplan(r.Context(), r.PathValue("id"), in.request(), in.ApprovalOperationID, in.JobSpec)
	if err != nil {
		httpError(w, err)
		return
	}
	if !decision.Allowed {
		jsonResponse(w, http.StatusConflict, map[string]any{"decision": goalProposalDecisionJSON(decision)})
		return
	}
	code := http.StatusAccepted
	if decision.Existing {
		code = http.StatusOK
	}
	jsonResponse(w, code, map[string]any{"job": j, "decision": goalProposalDecisionJSON(decision)})
}
