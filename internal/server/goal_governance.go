package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/governance"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/telemetry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) DecideGoal(ctx context.Context, jobID string, in governance.Request) (governance.Receipt, error) {
	var out governance.Receipt
	p, err := rpcutil.Require(ctx, "goals:approve", false)
	if err != nil {
		return out, err
	}
	if p.Identity.GoalActorKind != "human" || strings.TrimSpace(p.Identity.GoalActorID) == "" {
		return out, status.Error(codes.PermissionDenied, "GOAL_APPROVAL_UNAUTHORIZED")
	}
	if s.cfg.Maintenance {
		return out, status.Error(codes.Unavailable, "MAINTENANCE")
	}
	// Owner/project checks precede mutation, and are repeated by the governance transaction.
	j, err := s.jobAuthorized(ctx, jobID, "jobs:read")
	if err != nil {
		return out, err
	}
	err = s.db.Tx(ctx, func(q store.Query) error {
		current, e := readJob(ctx, q, j.ID)
		if e != nil {
			return e
		}
		goalID, e := ensureGoalBinding(ctx, q, current)
		if e != nil {
			return e
		}
		actor := governance.Actor{ID: p.Identity.GoalActorID, Kind: p.Identity.GoalActorKind, Owner: p.Identity.Owner, Projects: p.Identity.Projects, Scopes: p.Identity.Scopes}
		out, e = governance.DecideTx(ctx, q, actor, goalID, in)
		if e != nil {
			return e
		}
		if out.Existing {
			return nil
		}
		if in.Action == "ABORT" || in.Action == "REJECT" {
			// Mark the existing Job STOPPING atomically; established cleanup/release owns termination.
			if !terminal(current.State) && current.StopReason == "" {
				if e = jobState(ctx, q, current, "STOPPING", "USER_CANCEL", ""); e != nil {
					return e
				}
			}
		}
		return appendJobEvent(ctx, q, j.ID, "goal.governance_decided", out, "", "")
	})
	if err != nil {
		switch err.Error() {
		case "GOAL_APPROVAL_UNAUTHORIZED", "BUDGET_INCREASE_UNAUTHORIZED", "CONSTRAINT_CHANGE_UNAUTHORIZED":
			return out, status.Error(codes.PermissionDenied, err.Error())
		case "GOAL_NOT_FOUND":
			return out, status.Error(codes.NotFound, err.Error())
		case "STALE_GOAL_DECISION", "IDEMPOTENCY_CONFLICT":
			return out, status.Error(codes.Aborted, err.Error())
		case "GOAL_TERMINAL", "GOAL_APPROVAL_NOT_PENDING":
			return out, status.Error(codes.FailedPrecondition, err.Error())
		case "INVALID_GOAL_DECISION", "INVALID_GOAL_CHANGE", "INVALID_BUDGET_INCREASE", "UNSUPPORTED_GOAL_ACTION", "UNEXPECTED_BUDGET_CHANGE", "UNEXPECTED_GOAL_CHANGE", "EVIDENCE_REQUIRED":
			return out, status.Error(codes.InvalidArgument, err.Error())
		}
		return out, dbErr(err)
	}
	s.wake()
	return out, nil
}
func (s *Server) httpGoalDecision(w http.ResponseWriter, r *http.Request) {
	if err := s.requireControlWriteLease(r, r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	raw, err := readJSONBody(w, r, 16384)
	if err != nil {
		httpError(w, err)
		return
	}
	in, err := governance.DecodeRequest(raw)
	if err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "INVALID_GOAL_DECISION"))
		return
	}
	out, err := s.DecideGoal(r.Context(), r.PathValue("id"), in)
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, 200, out)
}

func goalGovernanceProjection(ctx context.Context, q store.Query, gid string) (map[string]any, error) {
	rows, err := q.QueryContext(ctx, `SELECT operation_id,actor,action,reason,resulting_version,created FROM goal_governance_decisions WHERE goal_id=? ORDER BY created DESC,operation_id DESC LIMIT 20`, gid)
	if err != nil {
		return nil, err
	}
	decisions := []map[string]any{}
	for rows.Next() {
		var id, actor, action, reason string
		var version, created int64
		if err = rows.Scan(&id, &actor, &action, &reason, &version, &created); err != nil {
			rows.Close()
			return nil, err
		}
		decisions = append(decisions, map[string]any{"operation_id": id, "actor": actor, "action": action, "reason": reason, "resulting_version": version, "created_at_ms": created})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var unknown int
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM goal_attempt_reservations r LEFT JOIN goal_usage u ON u.attempt_id=r.attempt_id WHERE r.goal_id=? AND (u.usage_id IS NULL OR u.complete=0)`, gid).Scan(&unknown); err != nil {
		return nil, err
	}
	supported, capabilities, err := projectedRuntimeBudgetSupport(ctx, q, gid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"recent_decisions": decisions, "decision_limit": 20, "usage_complete": unknown == 0, "runtime_hard_budget_supported": supported, "runtime_budget_capabilities": capabilities, "automatic_replan_enabled": false}, nil
}

func projectedRuntimeBudgetSupport(ctx context.Context, q store.Query, gid string) (bool, []string, error) {
	capabilities := []string{}
	var maxTokens, maxCost sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT max_tokens,max_cost_units FROM goal_budget_policy WHERE goal_id=?", gid).Scan(&maxTokens, &maxCost)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !maxTokens.Valid && !maxCost.Valid {
		return false, capabilities, nil
	}
	if err != nil {
		return false, nil, err
	}
	if maxTokens.Valid || !maxCost.Valid {
		return false, capabilities, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT j.spec FROM goal_job_bindings b JOIN goals g ON g.id=b.goal_id
		JOIN jobs j ON j.id=b.job_id WHERE b.goal_id=? AND b.plan_revision=g.active_plan_revision
		AND b.graph_generation=g.active_graph_generation`, gid)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return false, nil, err
		}
		var frozen job.Frozen
		if err = json.Unmarshal(raw, &frozen); err != nil {
			return false, nil, err
		}
		for _, execution := range frozen.Spec.Executions() {
			if execution.RuntimeProfile != "claude_http" {
				return false, capabilities, nil
			}
		}
	}
	if err = rows.Err(); err != nil {
		return false, nil, err
	}
	if !found {
		return false, capabilities, nil
	}
	return true, []string{claudeEstimatedUSDBudgetCapability}, nil
}

// Use exactly one token source per Attempt. Native cost is accepted only when
// the configured Runtime reports its complete final estimate.
func recordGoalAttemptUsage(ctx context.Context, q store.Query, j *Job, gid, aid string) error {
	u := governance.Usage{ID: "runtime-" + aid, AttemptID: aid, Source: "runtime-unavailable", Complete: false}
	var credential string
	if err := q.QueryRowContext(ctx, `SELECT json_extract(t.spec,'$.credential_ref') FROM attempts a JOIN tasks t ON t.id=a.task WHERE a.id=?`, aid).Scan(&credential); err != nil {
		return err
	}
	if j.frozen.Routes[credential] != "" {
		var tokens sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT sum(input_tokens+output_tokens) FROM gateway_requests WHERE attempt_id=?`, aid).Scan(&tokens); err != nil {
			return err
		}
		u.Source = "model-gateway"
		if tokens.Valid {
			u.Tokens = &tokens.Int64
		}
	} else {
		var raw []byte
		err := q.QueryRowContext(ctx, "SELECT payload FROM attempt_metrics WHERE attempt_id=?", aid).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(raw) > 0 {
			var m telemetry.Attempt
			if err = json.Unmarshal(raw, &m); err != nil {
				return err
			}
			u.Source = m.Source
			if m.Usage != nil {
				tokens := m.Usage.Input + m.Usage.Output
				u.Tokens = &tokens
			}
			u.CostUnits = m.CostUnits
			u.Complete = m.NativeFinal && m.UsageComplete && m.CostComplete && u.Tokens != nil && u.CostUnits != nil
		}
	}
	return governance.RecordUsageTx(ctx, q, gid, u)
}

func recordBoundGoalAttemptUsage(ctx context.Context, q store.Query, taskID, attemptID string) error {
	var jobID sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT job_id FROM tasks WHERE id=?", taskID).Scan(&jobID); err != nil {
		return err
	}
	if !jobID.Valid {
		return nil
	}
	j, err := readJob(ctx, q, jobID.String)
	if err != nil {
		return err
	}
	goalID, err := ensureGoalBinding(ctx, q, j)
	if err != nil {
		return err
	}
	return recordGoalAttemptUsage(ctx, q, j, goalID, attemptID)
}
