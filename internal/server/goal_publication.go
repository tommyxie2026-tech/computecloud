package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PublishGoalReplan is the staged Server-side publication path for an explicit
// proposal. It does not choose a Plan or enable automatic Re-plan. A trusted
// caller must provide both Guard evidence and a concrete Job spec whose
// canonical hash is committed in the proposed Plan.
func (s *Server) PublishGoalReplan(ctx context.Context, priorJobID string, in goal.ReplanRequest, approvalOperationID string, rawSpec []byte) (*Job, goal.ReplanDecision, error) {
	var empty goal.ReplanDecision
	if _, err := rpcutil.Require(ctx, "goals:propose", false); err != nil {
		return nil, empty, err
	}
	p, err := rpcutil.Require(ctx, "jobs:submit", false)
	if err != nil {
		return nil, empty, err
	}
	prior, err := s.jobAuthorized(ctx, priorJobID, "jobs:read")
	if err != nil {
		return nil, empty, err
	}
	if !s.cfg.Jobs.Enabled || s.cfg.Maintenance {
		return nil, empty, status.Error(codes.FailedPrecondition, "GOAL_PUBLICATION_UNAVAILABLE")
	}
	if len(rawSpec) > int(s.cfg.Jobs.MaxRequestBytes) {
		return nil, empty, status.Error(codes.InvalidArgument, "replan Job spec too large")
	}
	spec, err := job.Decode(rawSpec, s.cfg.Jobs.MaxPartitions, s.cfg.Jobs.MaxParallelism)
	if err != nil {
		return nil, empty, status.Error(codes.InvalidArgument, err.Error())
	}
	if spec.ProjectID != prior.project || !config.Contains(p.Identity.Projects, spec.ProjectID) {
		return nil, empty, status.Error(codes.PermissionDenied, "replan project not authorized")
	}
	requestHash := job.Hash(job.JSON(spec))
	if in.ProposedPlan.JobSpecHash == "" || in.ProposedPlan.JobSpecHash != requestHash {
		return nil, empty, status.Error(codes.InvalidArgument, "REPLAN_JOB_SPEC_MISMATCH")
	}
	if in.ID == "" || in.GoalID == "" || in.EvaluationID == "" || in.ExpectedPlanRevision < 1 || in.ExpectedGraphGeneration < 1 {
		return nil, empty, status.Error(codes.InvalidArgument, "replan Goal and evaluation required")
	}
	if in.Evidence.Validate() != nil || in.ProposedPlan.Validate() != nil || in.StrategyDelta.Validate() != nil || in.Progress.Validate() != nil {
		return nil, empty, status.Error(codes.InvalidArgument, "INVALID_GOAL_PROPOSAL")
	}
	if prior.State != "FAILED" || in.EvaluationID != fmt.Sprintf("%s:%d", prior.ID, prior.Version) {
		return nil, empty, status.Error(codes.FailedPrecondition, "REPLAN_EVALUATION_UNVERIFIED")
	}
	var evaluationBody []byte
	if err := s.db.SQL.QueryRowContext(ctx, `SELECT evidence_json FROM goal_evaluations
	 WHERE goal_id=? AND graph_generation=? AND job_id=? AND job_version=? AND verdict='FAILED'`,
		in.GoalID, in.ExpectedGraphGeneration, prior.ID, prior.Version).Scan(&evaluationBody); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, empty, status.Error(codes.FailedPrecondition, "REPLAN_EVALUATION_UNVERIFIED")
		}
		return nil, empty, dbErr(err)
	}
	if !proposalMatchesEvaluation(in.Evidence, evaluationBody, prior.ID, in.ExpectedPlanRevision) {
		return nil, empty, status.Error(codes.InvalidArgument, "REPLAN_EVIDENCE_UNVERIFIED")
	}
	jobID := store.ID()
	key := "goal-replan-" + job.Hash([]byte(in.GoalID + "/" + in.EvaluationID))[:32]
	decision, err := goal.GuardAndPublishReplan(ctx, s.db, in, approvalOperationID, func(ctx context.Context, q store.Query, d goal.ReplanDecision) error {
		var unsupportedBudget int
		if e := q.QueryRowContext(ctx, `SELECT count(*) FROM goal_budget_policy WHERE goal_id=?
 AND (max_tokens IS NOT NULL OR max_cost_units IS NOT NULL)`, in.GoalID).Scan(&unsupportedBudget); e != nil {
			return e
		}
		if unsupportedBudget != 0 {
			return fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
		}
		var goalID string
		var revision, generation int64
		if e := q.QueryRowContext(ctx, "SELECT goal_id,plan_revision,graph_generation FROM goal_job_bindings WHERE job_id=?", priorJobID).Scan(&goalID, &revision, &generation); e != nil {
			return e
		}
		if goalID != in.GoalID || revision != in.ExpectedPlanRevision || generation != in.ExpectedGraphGeneration {
			return fmt.Errorf("STALE_GOAL_JOB_BINDING")
		}
		var currentVersion int64
		var currentState, currentOwner, currentProject string
		if e := q.QueryRowContext(ctx, "SELECT state,version,owner,project FROM jobs WHERE id=?", priorJobID).Scan(&currentState, &currentVersion, &currentOwner, &currentProject); e != nil {
			return e
		}
		if currentState != "FAILED" || currentVersion != prior.Version || currentOwner != prior.owner || currentProject != prior.project {
			return fmt.Errorf("REPLAN_EVALUATION_STALE")
		}
		frozen := job.Frozen{Spec: spec, Digests: map[string]string{}, Routes: map[string]string{}, RouteDigests: map[string]string{}}
		for _, ex := range spec.Executions() {
			if !config.Contains(p.Identity.Credentials, ex.CredentialRef) || s.cfg.Credentials[ex.CredentialRef] < 1 {
				return status.Error(codes.PermissionDenied, "credential not authorized")
			}
			templateKey := job.TemplateKey(ex)
			for _, t := range s.cfg.Jobs.Templates {
				if t.Key() == templateKey {
					frozen.Digests[templateKey] = t.Digest
					break
				}
			}
			if frozen.Digests[templateKey] == "" {
				return status.Error(codes.FailedPrecondition, "TEMPLATE_NOT_CONFIGURED")
			}
			if route := s.cfg.ModelGateway.CredentialRoutes[ex.CredentialRef]; route != "" {
				if !s.cfg.ModelGateway.Enabled || ex.Engine != "codex" || !config.Contains(s.cfg.ModelGateway.Routes[route].AllowedModels, ex.Model) {
					return status.Error(codes.FailedPrecondition, "GATEWAY_ROUTE_UNAVAILABLE")
				}
				frozen.Routes[ex.CredentialRef] = route
				frozen.RouteDigests[route] = config.RouteDigest(s.cfg.ModelGateway.Routes[route])
			}
		}
		additional := 1
		if spec.Map != nil {
			additional = len(spec.Map.Partitions)
		}
		if e := s.checkQueueAdmission(ctx, q, spec.ProjectID, additional); e != nil {
			return e
		}
		traceID := "trc_" + store.ID()
		if e := createTrace(ctx, q, traceID, prior.owner, prior.project, "job"); e != nil {
			return e
		}
		now := store.Now()
		deadline := now + spec.Limits.TimeoutSeconds*1000
		var goalDeadline, created, maxWall int64
		if e := q.QueryRowContext(ctx, "SELECT deadline,created,max_wall_time_ms FROM goals WHERE id=?", in.GoalID).Scan(&goalDeadline, &created, &maxWall); e != nil {
			return e
		}
		if goalDeadline > 0 && deadline > goalDeadline {
			deadline = goalDeadline
		}
		if maxWall > 0 && deadline > created+maxWall {
			deadline = created + maxWall
		}
		if deadline <= now {
			return fmt.Errorf("GOAL_DEADLINE_EXCEEDED")
		}
		parallel := 1
		if spec.Map != nil {
			parallel = spec.Map.Parallelism
		}
		frozenBytes := job.JSON(frozen)
		specHash := job.Hash(job.JSON(struct {
			Frozen   job.Frozen
			Deadline int64
		}{frozen, deadline}))
		if _, e := q.ExecContext(ctx, `INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism,trace_id)
 VALUES(?,?,?,?,?,?,?,?,'QUEUED',?,?,?,?,?)`, jobID, prior.owner, prior.project, key, requestHash, specHash, frozenBytes, spec.Mode, now, now, deadline, parallel, traceID); e != nil {
			return e
		}
		if spec.Mode == "single" {
			if _, e := insertStage(ctx, q, jobID, "single", 0, "READY"); e != nil {
				return e
			}
			if e := s.insertJobTask(ctx, q, jobID, prior.owner, deadline, "single", "_single", spec, *spec.Execution, spec.Input.Text, frozen); e != nil {
				return e
			}
		} else {
			if _, e := insertStage(ctx, q, jobID, "map", 0, "READY"); e != nil {
				return e
			}
			if _, e := insertStage(ctx, q, jobID, "reduce", 1, "PENDING"); e != nil {
				return e
			}
			for _, part := range spec.Map.Partitions {
				if e := s.insertJobTask(ctx, q, jobID, prior.owner, deadline, "map", part.Key, spec, part.Execution, spec.Input.Text+"\n\n"+part.Input.Text, frozen); e != nil {
					return e
				}
			}
		}
		if _, e := q.ExecContext(ctx, "INSERT INTO goal_job_bindings VALUES(?,?,?,?)", jobID, in.GoalID, d.NextPlanRevision, d.NextGraphGeneration); e != nil {
			return e
		}
		if _, e := q.ExecContext(ctx, "INSERT INTO goal_plans VALUES(?,?,?,?,?,?,?)", in.GoalID, d.NextPlanRevision, d.NextGraphGeneration, jobID, frozenBytes, job.JSON(compileJobGraph(spec)), now); e != nil {
			return e
		}
		return appendJobEvent(ctx, q, jobID, "job.created", map[string]any{"mode": spec.Mode, "goal_id": in.GoalID, "plan_revision": d.NextPlanRevision}, "", "")
	})
	if err != nil {
		switch err.Error() {
		case "IDEMPOTENCY_CONFLICT":
			return nil, decision, status.Error(codes.AlreadyExists, err.Error())
		case "STALE_GOAL_JOB_BINDING", "REPLAN_EVALUATION_STALE":
			return nil, decision, status.Error(codes.Aborted, err.Error())
		case "INVALID_REPLAN_PERMISSION", "REPLAN_PERMISSION_UNAVAILABLE", "REPLAN_PERMISSION_NOT_REQUIRED", "REPLAN_PUBLICATION_REPLAY_UNVERIFIED", "GOAL_RUNTIME_BUDGET_UNSUPPORTED", "GOAL_DEADLINE_EXCEEDED":
			return nil, decision, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, decision, dbErr(err)
	}
	if !decision.Allowed {
		return nil, decision, nil
	}
	if decision.Existing {
		err = s.db.SQL.QueryRowContext(ctx, "SELECT job_id FROM goal_plans WHERE goal_id=? AND revision=? AND graph_generation=?", in.GoalID, decision.NextPlanRevision, decision.NextGraphGeneration).Scan(&jobID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, decision, fmt.Errorf("REPLAN_PUBLICATION_REPLAY_UNVERIFIED")
			}
			return nil, decision, dbErr(err)
		}
	}
	s.wake()
	created, err := readJob(ctx, s.db.SQL, jobID)
	if err != nil {
		return nil, decision, dbErr(err)
	}
	created.Existing = decision.Existing
	return created, decision, nil
}

// The initial explicit proposal path accepts only the failure fact persisted by
// the evaluator. Richer artifact facts need their own verifiable provenance.
func proposalMatchesEvaluation(in goal.ReplanEvidence, raw []byte, jobID string, revision int64) bool {
	var record struct {
		JobID        string `json:"job_id"`
		JobState     string `json:"job_state"`
		Reason       string `json:"reason"`
		PlanRevision int64  `json:"plan_revision"`
	}
	if json.Unmarshal(raw, &record) != nil || record.JobID != jobID || record.PlanRevision != revision || record.JobState != "FAILED" {
		return false
	}
	reason := strings.TrimSpace(record.Reason)
	if reason == "" {
		reason = "JOB_FAILED"
	}
	class := goal.FailureUnknown
	switch reason {
	case "TEST_FAILURE":
		class = goal.FailureTest
	case "TIMEOUT":
		class = goal.FailureTimeout
	case "CAPABILITY_UNAVAILABLE":
		class = goal.FailureMissingCapability
	case "PERMISSION_DENIED":
		class = goal.FailurePermissionDenied
	case "RESOURCE_EXHAUSTED":
		class = goal.FailureResourceExhausted
	}
	return in.FailureClass == class && len(in.Evidence) == 1 &&
		in.Evidence[0].Type == "job_evaluation" && in.Evidence[0].ArtifactID == "" &&
		in.Evidence[0].Fact == reason
}
