package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func graphEvaluationFact(ev GraphEvaluation) string {
	return "output=" + ev.OutputHash + ";evidence=" + ev.EvidenceHash + ";verdict=" + ev.Verdict
}

func (s *Server) publishGraphReplan(ctx context.Context, prior *Job, in goal.ReplanRequest, approval string, raw []byte, p rpcutil.Principal) (*Job, goal.ReplanDecision, error) {
	if prior.graph.Plan.Spec.Version == graph.CoverageVersion {
		return nil, goal.ReplanDecision{}, status.Error(codes.FailedPrecondition, "V5_RECOVERY_PROPOSAL_REQUIRED")
	}
	return s.publishGraphReplanWithRecovery(ctx, prior, in, approval, raw, p, nil)
}
func (s *Server) publishGraphReplanWithRecovery(ctx context.Context, prior *Job, in goal.ReplanRequest, approval string, raw []byte, p rpcutil.Principal, recovery *graph.RecoveryProposal) (*Job, goal.ReplanDecision, error) {
	var empty goal.ReplanDecision
	if !s.cfg.Jobs.Enabled || !s.cfg.Jobs.GraphsEnabled || s.cfg.Maintenance {
		return nil, empty, status.Error(codes.FailedPrecondition, "GRAPH_PUBLICATION_UNAVAILABLE")
	}
	if len(raw) > int(s.cfg.Jobs.MaxRequestBytes) {
		return nil, empty, status.Error(codes.InvalidArgument, "GRAPH_REQUEST_LIMIT")
	}
	f, e := s.freezeGraph(raw, p)
	if e != nil {
		return nil, empty, e
	}
	spec := f.Plan.Spec
	if spec.ProjectID != prior.project || f.Plan.Digest != in.ProposedPlan.JobSpecHash || in.GoalID != prior.graph.GoalID || in.ExpectedGraphGeneration != prior.graph.Generation {
		return nil, empty, status.Error(codes.InvalidArgument, "GRAPH_PROPOSAL_MISMATCH")
	}
	if !terminal(prior.State) || prior.State == "CANCELED" {
		return nil, empty, status.Error(codes.FailedPrecondition, "GRAPH_NOT_EVALUATED")
	}
	var evBody []byte
	var currentID string
	if e = s.db.SQL.QueryRowContext(ctx, `SELECT e.body,x.evaluation_id FROM iu_evaluations e JOIN iu_graphs x ON x.goal_id=e.goal_id AND x.generation=e.generation WHERE e.id=? AND e.goal_id=? AND e.generation=? AND e.verdict='FAIL' AND x.job_id=?`, in.EvaluationID, in.GoalID, in.ExpectedGraphGeneration, prior.ID).Scan(&evBody, &currentID); e != nil {
		return nil, empty, status.Error(codes.FailedPrecondition, "GRAPH_EVALUATION_UNVERIFIED")
	}
	var ev GraphEvaluation
	if e = json.Unmarshal(evBody, &ev); e != nil {
		return nil, empty, e
	}
	class := goal.FailureTest
	if prior.State == "FAILED" {
		class = goal.FailureUnknown
	}
	if currentID != in.EvaluationID || in.Evidence.FailureClass != class || len(in.Evidence.Evidence) != 1 || in.Evidence.Evidence[0].Type != "iu_graph_evaluation" || in.Evidence.Evidence[0].ArtifactID != "" || in.Evidence.Evidence[0].Fact != graphEvaluationFact(ev) {
		return nil, empty, status.Error(codes.InvalidArgument, "GRAPH_EVIDENCE_UNVERIFIED")
	}
	if (prior.graph.Plan.Spec.Limits.MaxCostUnits == nil) != (spec.Limits.MaxCostUnits == nil) {
		return nil, empty, status.Error(codes.FailedPrecondition, "GRAPH_BUDGET_MODE_FROZEN")
	}
	id := store.ID()
	f.GoalID = in.GoalID
	decision, e := goal.GuardAndPublishReplan(ctx, s.db, in, approval, func(ctx context.Context, q store.Query, d goal.ReplanDecision) error {
		if recovery != nil {
			if e := s.checkRecoveryTx(ctx, q, prior, *recovery, true); e != nil {
				return e
			}
		}
		if _, e := graphMutationAuthorityTx(ctx, q, prior, "goals:propose"); e != nil {
			return e
		}
		var pointer, hash string
		var revision, generation, version int64
		if e := q.QueryRowContext(ctx, `SELECT x.evaluation_id,x.output_hash,b.plan_revision,b.graph_generation,j.version FROM iu_graphs x JOIN jobs j ON j.id=x.job_id JOIN goal_job_bindings b ON b.job_id=j.id WHERE x.job_id=?`, prior.ID).Scan(&pointer, &hash, &revision, &generation, &version); e != nil {
			return e
		}
		if pointer != in.EvaluationID || hash != ev.OutputHash || revision != in.ExpectedPlanRevision || generation != in.ExpectedGraphGeneration || version != prior.Version {
			return fmt.Errorf("GRAPH_EVALUATION_STALE")
		}
		validityRevision, validityErr := graphValidityRevisionTx(ctx, q, prior.graph)
		if validityErr != nil {
			return validityErr
		}
		if validityRevision != ev.ExpectedValidityRevision {
			return fmt.Errorf("GRAPH_VALIDITY_STALE")
		}
		var active int
		if e := q.QueryRowContext(ctx, `SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task JOIN goal_job_bindings b ON b.job_id=t.job_id WHERE b.goal_id=? AND a.released=0`, in.GoalID).Scan(&active); e != nil {
			return e
		}
		if active != 0 {
			return fmt.Errorf("GRAPH_EXECUTION_IN_FLIGHT")
		}
		var maxCost *int64
		var deadline int64
		if e := q.QueryRowContext(ctx, `SELECT g.deadline,p.max_cost_units FROM goals g LEFT JOIN goal_budget_policy p ON p.goal_id=g.id WHERE g.id=?`, in.GoalID).Scan(&deadline, &maxCost); e != nil {
			return e
		}
		if (maxCost == nil) != (spec.Limits.MaxCostUnits == nil) || maxCost != nil && *maxCost != *spec.Limits.MaxCostUnits {
			return fmt.Errorf("GRAPH_BUDGET_POLICY_MISMATCH")
		}
		now := store.Now()
		if specDeadline := now + spec.Limits.TimeoutSeconds*1000; deadline == 0 || deadline > specDeadline {
			deadline = specDeadline
		}
		if deadline <= now {
			return fmt.Errorf("GOAL_DEADLINE_EXCEEDED")
		}
		if e := s.checkQueueAdmission(ctx, q, spec.ProjectID, len(spec.Nodes)); e != nil {
			return e
		}
		traceID := "trc_" + store.ID()
		if e := createTrace(ctx, q, traceID, prior.owner, prior.project, "job"); e != nil {
			return e
		}
		f.Generation = d.NextGraphGeneration
		frozen := job.JSON(f)
		if _, e := q.ExecContext(ctx, `INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism,trace_id) VALUES(?,?,?,?,?,?,?,'iu_graph','QUEUED',?,?,?,?,?)`, id, prior.owner, prior.project, "graph-replan-"+job.Hash([]byte(in.EvaluationID)), f.Plan.Digest, job.Hash(frozen), frozen, now, now, deadline, f.Plan.EffectiveParallelism, traceID); e != nil {
			return e
		}
		if _, e := q.ExecContext(ctx, `INSERT INTO goal_job_bindings VALUES(?,?,?,?)`, id, in.GoalID, d.NextPlanRevision, d.NextGraphGeneration); e != nil {
			return e
		}
		if _, e := q.ExecContext(ctx, `INSERT INTO goal_plans VALUES(?,?,?,?,?,?,?)`, in.GoalID, d.NextPlanRevision, d.NextGraphGeneration, id, frozen, job.JSON(spec), now); e != nil {
			return e
		}
		if _, e := q.ExecContext(ctx, `INSERT INTO iu_graphs(job_id,goal_id,generation,plan) VALUES(?,?,?,?)`, id, in.GoalID, d.NextGraphGeneration, frozen); e != nil {
			return e
		}
		for _, n := range spec.Nodes {
			if _, e := q.ExecContext(ctx, `INSERT INTO iu_nodes(goal_id,generation,node_id) VALUES(?,?,?)`, in.GoalID, d.NextGraphGeneration, n.ID); e != nil {
				return e
			}
		}
		target := &Job{ID: id, owner: prior.owner, project: prior.project, graph: &f}
		if e := bindGraphImportsTx(ctx, q, target); e != nil {
			return e
		}
		if recovery != nil {
			if _, e := q.ExecContext(ctx, `INSERT INTO iu_recovery_publications VALUES(?,?,?,?,?,?)`, recovery.GoalID, recovery.ID, prior.graph.Generation, f.Generation, id, store.Now()); e != nil {
				return e
			}
		}
		return appendJobEvent(ctx, q, id, "graph.created", map[string]any{"goal_id": in.GoalID, "graph_generation": d.NextGraphGeneration, "evaluation_id": in.EvaluationID}, "", "")
	})
	if e != nil {
		return nil, decision, e
	}
	if !decision.Allowed {
		return nil, decision, nil
	}
	if decision.Existing {
		if e = s.db.SQL.QueryRowContext(ctx, `SELECT job_id FROM goal_plans WHERE goal_id=? AND revision=? AND graph_generation=?`, in.GoalID, decision.NextPlanRevision, decision.NextGraphGeneration).Scan(&id); e != nil {
			return nil, decision, e
		}
	}
	s.wake()
	next, e := readJob(ctx, s.db.SQL, id)
	if next != nil {
		next.Existing = decision.Existing
	}
	return next, decision, e
}
