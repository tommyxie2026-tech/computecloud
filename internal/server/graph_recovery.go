package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
)

func (s *Server) checkRecoveryTx(ctx context.Context, q store.Query, prior *Job, p graph.RecoveryProposal, publishing bool) error {
	f := prior.graph
	if _, e := graphMutationAuthorityTx(ctx, q, prior, "goals:propose"); e != nil {
		return e
	}
	if _, e := graphMutationAuthorityTx(ctx, q, prior, "jobs:submit"); e != nil {
		return e
	}
	if f == nil || p.GoalID != f.GoalID || p.PriorGeneration != f.Generation || p.PriorPlanHash != f.Plan.Digest {
		return importFailure("RECOVERY_PLAN_STALE")
	}
	var evalID, goalState string
	var activeGeneration int64
	var evRaw []byte
	if e := q.QueryRowContext(ctx, `SELECT x.evaluation_id,e.body,g.active_graph_generation,g.state FROM iu_graphs x JOIN iu_evaluations e ON e.id=x.evaluation_id JOIN goals g ON g.id=x.goal_id WHERE x.job_id=?`, prior.ID).Scan(&evalID, &evRaw, &activeGeneration, &goalState); e != nil {
		return importFailure("RECOVERY_FAIL_REQUIRED")
	}
	if (!publishing && (activeGeneration != f.Generation || goalState != "REPLAN_GUARDING" && goalState != "NEEDS_APPROVAL")) || (publishing && (activeGeneration != f.Generation+1 || goalState != "REPLANNING")) {
		return importFailure("RECOVERY_FAIL_REQUIRED")
	}
	var ev GraphEvaluation
	if json.Unmarshal(evRaw, &ev) != nil || ev.Verdict != "FAIL" || evalID != p.EvaluationID || graph.Hash(evRaw) != p.EvaluationHash || ev.CoverageProof == nil || ev.CoverageProof.ReceiptID != p.CoverageReceiptID || ev.CoverageProof.SHA256 != p.CoverageReceiptHash {
		return importFailure("RECOVERY_FAIL_REQUIRED")
	}
	if e := s.validateGraphCoverageProofTx(ctx, q, f, ev); e != nil {
		return e
	}
	var receiptRaw []byte
	if e := q.QueryRowContext(ctx, `SELECT body FROM iu_coverage_receipts WHERE goal_id=? AND generation=? AND receipt_id=?`, f.GoalID, f.Generation, p.CoverageReceiptID).Scan(&receiptRaw); e != nil {
		return e
	}
	var proof GraphCoverageReceipt
	if json.Unmarshal(receiptRaw, &proof) != nil || proof.Verdict != "FAIL" {
		return importFailure("RECOVERY_FAIL_REQUIRED")
	}
	var count int
	if e := q.QueryRowContext(ctx, `SELECT count(*) FROM iu_recovery_publications WHERE goal_id=?`, f.GoalID).Scan(&count); e != nil {
		return e
	}
	if count >= 2 {
		return status.Error(codes.ResourceExhausted, "RECOVERY_LIMIT")
	}
	if len(p.ReadReceiptIDs) < 1 || len(p.ReadReceiptIDs) > 8 {
		return importFailure("RECOVERY_READ_LIMIT")
	}
	wanted := map[string]bool{}
	for _, id := range p.ReadReceiptIDs {
		if !job.Ref(id) || wanted[id] {
			return importFailure("RECOVERY_READ_ID")
		}
		wanted[id] = true
	}
	rows, e := q.QueryContext(ctx, `SELECT body FROM iu_source_reads WHERE goal_id=? AND generation=?`, f.GoalID, f.Generation)
	if e != nil {
		return e
	}
	reads := map[string][]graph.Span{}
	found := map[string]bool{}
	total := 0
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return e
		}
		var r GraphSourceReadReceipt
		if e = json.Unmarshal(raw, &r); e != nil {
			rows.Close()
			return e
		}
		if !wanted[r.ReceiptID] {
			continue
		}
		if found[r.ReceiptID] || r.PlanHash != f.Plan.Digest || r.GoalID != f.GoalID || r.Generation != f.Generation || r.SHA256 != graph.Hash([]byte(r.Text)) {
			rows.Close()
			return importFailure("RECOVERY_READ_STALE")
		}
		found[r.ReceiptID] = true
		total += len(r.Text)
		reads[r.NodeID] = append(reads[r.NodeID], r.Span)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(found) != len(wanted) || total > 64<<10 {
		return importFailure("RECOVERY_READ_MISSING_OR_LIMIT")
	}
	old := map[string]graph.Node{}
	for _, n := range f.Plan.Spec.Nodes {
		old[n.ID] = n
	}
	usedNode := map[string]bool{}
	for _, n := range p.ProposedSpec.Nodes {
		base := old[n.ID]
		if n.Instructions != base.Instructions && len(reads[n.ID]) == 0 {
			return importFailure("RECOVERY_INSTRUCTION_NEEDS_EVIDENCE")
		}
		selected := append(append(append([]graph.Span{}, p.ProposedSpec.Rules...), n.Rules...), n.Spans...)
		for _, sp := range reads[n.ID] {
			if !serverSpanCovered(sp, selected) {
				return importFailure("RECOVERY_READ_NOT_LOADED")
			}
			usedNode[n.ID] = true
		}
		for _, pair := range [][2][]graph.Span{{base.Spans, n.Spans}, {base.Rules, n.Rules}} {
			for _, sp := range pair[1] {
				if !serverSpanCovered(sp, pair[0]) && !serverSpanCovered(sp, reads[n.ID]) {
					return importFailure("RECOVERY_NEW_SPAN_UNAUTHORIZED")
				}
			}
		}
	}
	for node := range reads {
		if !usedNode[node] {
			return importFailure("RECOVERY_READ_NODE_MISSING")
		}
	}
	return nil
}
func serverSpanCovered(sp graph.Span, spans []graph.Span) bool { // The source content was validated at Compile; interval coverage is sufficient here.
	end := sp.Start
	for end < sp.End {
		next := end
		for _, v := range spans {
			if v.Source == sp.Source && v.SHA256 == sp.SHA256 && v.Start <= end && v.End > next {
				next = v.End
			}
		}
		if next == end {
			return false
		}
		end = next
	}
	return true
}
func (s *Server) RecoverGraph(ctx context.Context, id string, p graph.RecoveryProposal, approval string) (*Job, goal.ReplanDecision, error) {
	var empty goal.ReplanDecision
	if p.Version != "iu-recovery-proposal-v1" || !job.ValidKey(p.ID) || p.SHA256 != p.Digest() || len(job.JSON(p)) > 320<<10 || len(job.JSON(p.ProposedSpec)) > graph.MaxRequestBytes {
		return nil, empty, status.Error(codes.InvalidArgument, "RECOVERY_PROPOSAL_INVALID")
	}
	principal, e := rpcutil.Require(ctx, "jobs:submit", false)
	if e != nil {
		return nil, empty, e
	}
	if _, e = rpcutil.Require(ctx, "goals:propose", false); e != nil {
		return nil, empty, e
	}
	prior, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, empty, e
	}
	if prior.graph == nil || prior.graph.GoalID != p.GoalID {
		return nil, empty, importFailure("RECOVERY_GRAPH_REQUIRED")
	}
	var replayID string
	e = s.db.Tx(ctx, func(q store.Query) error {
		if _, e := graphMutationAuthorityTx(ctx, q, prior, "goals:propose"); e != nil {
			return e
		}
		if _, e := graphMutationAuthorityTx(ctx, q, prior, "jobs:submit"); e != nil {
			return e
		}
		var raw []byte
		e := q.QueryRowContext(ctx, `SELECT request FROM iu_recovery_proposals WHERE goal_id=? AND proposal_id=?`, p.GoalID, p.ID).Scan(&raw)
		if e == nil {
			if !bytes.Equal(raw, job.JSON(p)) {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			e = q.QueryRowContext(ctx, `SELECT job_id FROM iu_recovery_publications WHERE goal_id=? AND proposal_id=?`, p.GoalID, p.ID).Scan(&replayID)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		return nil
	})
	if e != nil {
		return nil, empty, e
	}
	if replayID != "" {
		j, e := readJob(ctx, s.db.SQL, replayID)
		if j != nil {
			j.Existing = true
		}
		return j, goal.ReplanDecision{Allowed: true, Existing: true, Code: goal.DecisionAllowed, NextGraphGeneration: jGraphGeneration(j), NextPlanRevision: jGraphGeneration(j)}, e
	}
	if e = graph.ValidateRecovery(prior.graph.Plan.Spec, p.ProposedSpec); e != nil {
		return nil, empty, e
	}
	frozen, e := s.freezeGraph(job.JSON(p.ProposedSpec), principal)
	if e != nil {
		return nil, empty, e
	}
	if !bytes.Equal(job.JSON(frozen.Digests), job.JSON(prior.graph.Digests)) || (len(frozen.Commands) != len(prior.graph.Commands) || len(frozen.Commands) > 0 && !bytes.Equal(job.JSON(frozen.Commands), job.JSON(prior.graph.Commands))) || !bytes.Equal(job.JSON(frozen.CoverageVerifier), job.JSON(prior.graph.CoverageVerifier)) || (len(frozen.ReplayBindings) != len(prior.graph.ReplayBindings) || len(frozen.ReplayBindings) > 0 && !bytes.Equal(job.JSON(frozen.ReplayBindings), job.JSON(prior.graph.ReplayBindings))) {
		return nil, empty, importFailure("RECOVERY_BINDING_DRIFT")
	}
	var ev GraphEvaluation
	var receipt GraphCoverageReceipt
	var revision int64
	e = s.db.Tx(ctx, func(q store.Query) error {
		if e := s.checkRecoveryTx(ctx, q, prior, p, false); e != nil {
			return e
		}
		var raw []byte
		if e := q.QueryRowContext(ctx, `SELECT body FROM iu_evaluations WHERE id=?`, p.EvaluationID).Scan(&raw); e != nil {
			return e
		}
		if e := json.Unmarshal(raw, &ev); e != nil {
			return e
		}
		if e := q.QueryRowContext(ctx, `SELECT body FROM iu_coverage_receipts WHERE goal_id=? AND generation=? AND receipt_id=?`, p.GoalID, p.PriorGeneration, p.CoverageReceiptID).Scan(&raw); e != nil {
			return e
		}
		if e := json.Unmarshal(raw, &receipt); e != nil {
			return e
		}
		if e := q.QueryRowContext(ctx, `SELECT plan_revision FROM goal_job_bindings WHERE job_id=?`, id).Scan(&revision); e != nil {
			return e
		}
		var existing []byte
		e := q.QueryRowContext(ctx, `SELECT request FROM iu_recovery_proposals WHERE goal_id=? AND proposal_id=?`, p.GoalID, p.ID).Scan(&existing)
		if e == nil {
			if !bytes.Equal(existing, job.JSON(p)) {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var count int
		if e = q.QueryRowContext(ctx, `SELECT count(*) FROM iu_recovery_proposals WHERE goal_id=?`, p.GoalID).Scan(&count); e != nil {
			return e
		}
		if count >= 128 {
			return status.Error(codes.ResourceExhausted, "RECOVERY_PROPOSAL_LIMIT")
		}
		actor, e := graphMutationAuthorityTx(ctx, q, prior, "goals:propose")
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO iu_recovery_proposals VALUES(?,?,?,?,?,?)`, p.GoalID, p.ID, p.SHA256, job.JSON(p), actor, store.Now())
		return e
	})
	if e != nil {
		return nil, empty, e
	}
	progress := goal.ProgressSnapshot{}
	for _, ck := range receipt.Checks {
		switch ck.Verdict {
		case "PASS":
			progress.AcceptedChecks++
		case "FAIL":
			progress.FailedChecks++
		default:
			progress.UnknownChecks++
		}
	}
	progress.UnresolvedBlockers = progress.FailedChecks + progress.UnknownChecks
	plan, e := graph.Compile(p.ProposedSpec)
	if e != nil {
		return nil, empty, e
	}
	in := goal.ReplanRequest{ID: p.ID, GoalID: p.GoalID, EvaluationID: p.EvaluationID, ExpectedPlanRevision: revision, ExpectedGraphGeneration: p.PriorGeneration, ReasonCode: "IU_EVIDENCE_RECOVERY", Evidence: goal.ReplanEvidence{FailureClass: goal.FailureTest, Evidence: []goal.Evidence{{Type: "iu_graph_evaluation", Fact: graphEvaluationFact(ev)}}}, ProposedPlan: goal.PlanCanonical{Strategy: "iu-evidence-recovery/" + p.SHA256, StrategyClass: "iu-evidence-recovery", DependencySignature: plan.Digest, EvaluationStrategy: receipt.Profile.Version, SideEffectClass: "read_only", JobSpecHash: plan.Digest}, StrategyDelta: goal.StrategyDelta{ChangedDimensions: []string{"evidence_spans", "instructions"}, Summary: "Supplement bounded frozen evidence and repeat independent coverage evaluation"}, Progress: progress}
	return s.publishGraphReplanWithRecovery(ctx, prior, in, approval, job.JSON(p.ProposedSpec), principal, &p)
}
func jGraphGeneration(j *Job) int64 {
	if j != nil && j.graph != nil {
		return j.graph.Generation
	}
	return 0
}
func (s *Server) httpRecoverGraph(w http.ResponseWriter, r *http.Request) {
	if e := s.requireControlWriteLease(r, r.PathValue("id")); e != nil {
		httpError(w, e)
		return
	}
	raw, e := readJSONBody(w, r, 320<<10)
	if e != nil {
		httpError(w, e)
		return
	}
	var in struct {
		Proposal graph.RecoveryProposal `json:"proposal"`
		Approval string                 `json:"approval_operation_id"`
	}
	if e = jsonutil.Decode(raw, &in); e != nil {
		httpError(w, status.Error(codes.InvalidArgument, "RECOVERY_REQUEST_INVALID"))
		return
	}
	j, d, e := s.RecoverGraph(r.Context(), r.PathValue("id"), in.Proposal, in.Approval)
	if e != nil {
		httpError(w, e)
		return
	}
	code := http.StatusAccepted
	if !d.Allowed {
		code = http.StatusConflict
	} else if d.Existing {
		code = http.StatusOK
	}
	jsonResponse(w, code, map[string]any{"job": j, "decision": goalProposalDecisionJSON(d)})
}
