// Package governance persists human decisions and usage; it never launches work.
package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type Actor struct {
	ID, Kind, Owner  string
	Projects, Scopes []string
}
type Change struct {
	MaxAttempts  *int64 `json:"max_attempts,omitempty"`
	MaxReplans   *int64 `json:"max_replans,omitempty"`
	MaxTokens    *int64 `json:"max_tokens,omitempty"`
	MaxCostUnits *int64 `json:"max_cost_units,omitempty"`
	Evidence     string `json:"evidence,omitempty"`
	Constraint   string `json:"constraint,omitempty"`
}
type Request struct {
	OperationID     string `json:"operation_id"`
	ExpectedVersion int64  `json:"expected_version"`
	PlanRevision    int64  `json:"plan_revision"`
	GraphGeneration int64  `json:"graph_generation"`
	Action          string `json:"action"`
	Reason          string `json:"reason"`
	Change          Change `json:"change"`
}
type Receipt struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	Actor       string `json:"actor"`
	Version     int64  `json:"resulting_version"`
	Existing    bool   `json:"existing"`
}

func has(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func fail(code string) error { return errors.New(code) }
func terminal(s string) bool { return s == "SUCCEEDED" || s == "FAILED" || s == "CANCELED" }

// DecideTx must share the server transaction for any associated Job cancellation.
// APPROVE_NEXT_REPLAN creates only a one-use permission; publication consumes it.
func DecideTx(ctx context.Context, q store.Query, actor Actor, goalID string, in Request) (Receipt, error) {
	out := Receipt{OperationID: in.OperationID, Action: in.Action, Actor: actor.ID}
	if actor.Kind != "human" || strings.TrimSpace(actor.ID) == "" || !has(actor.Scopes, "goals:approve") {
		return out, fail("GOAL_APPROVAL_UNAUTHORIZED")
	}
	if !job.ValidKey(in.OperationID) || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2048 || in.ExpectedVersion < 1 || in.PlanRevision < 1 || in.GraphGeneration < 1 {
		return out, fail("INVALID_GOAL_DECISION")
	}
	if len(in.Change.Evidence) > 4096 || len(in.Change.Constraint) > 4096 {
		return out, fail("INVALID_GOAL_CHANGE")
	}
	switch in.Action {
	case "APPROVE_NEXT_REPLAN", "INCREASE_BUDGET", "PROVIDE_EVIDENCE", "CHANGE_CONSTRAINT", "REJECT", "ABORT":
	default:
		return out, fail("UNSUPPORTED_GOAL_ACTION")
	}
	var owner, project, state string
	var version, revision, generation, maxAttempts, maxReplans int64
	if err := q.QueryRowContext(ctx, "SELECT owner,project,state,version,active_plan_revision,active_graph_generation,max_total_attempts,max_replans FROM goals WHERE id=?", goalID).Scan(&owner, &project, &state, &version, &revision, &generation, &maxAttempts, &maxReplans); err != nil {
		return out, err
	}
	if owner != actor.Owner || !has(actor.Projects, project) {
		return out, fail("GOAL_NOT_FOUND")
	}
	digest := job.Hash(job.JSON(struct {
		Actor   string
		Request Request
	}{actor.ID, in}))
	var previous string
	err := q.QueryRowContext(ctx, "SELECT request_hash,resulting_version FROM goal_governance_decisions WHERE goal_id=? AND operation_id=?", goalID, in.OperationID).Scan(&previous, &out.Version)
	if err == nil {
		if previous != digest {
			return out, fail("IDEMPOTENCY_CONFLICT")
		}
		out.Existing = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if terminal(state) {
		return out, fail("GOAL_TERMINAL")
	}
	if version != in.ExpectedVersion || revision != in.PlanRevision || generation != in.GraphGeneration {
		return out, fail("STALE_GOAL_DECISION")
	}
	if in.Action != "INCREASE_BUDGET" && in.Action != "ABORT" && state != "NEEDS_APPROVAL" {
		return out, fail("GOAL_APPROVAL_NOT_PENDING")
	}
	changedBudget := in.Change.MaxAttempts != nil || in.Change.MaxReplans != nil || in.Change.MaxTokens != nil || in.Change.MaxCostUnits != nil
	if in.Action != "INCREASE_BUDGET" && changedBudget {
		return out, fail("UNEXPECTED_BUDGET_CHANGE")
	}
	if in.Action != "PROVIDE_EVIDENCE" && in.Change.Evidence != "" || in.Action != "CHANGE_CONSTRAINT" && in.Change.Constraint != "" {
		return out, fail("UNEXPECTED_GOAL_CHANGE")
	}
	switch in.Action {
	case "INCREASE_BUDGET":
		if !has(actor.Scopes, "goals:budget") || !changedBudget {
			return out, fail("BUDGET_INCREASE_UNAUTHORIZED")
		}
		if in.Change.MaxAttempts != nil {
			if *in.Change.MaxAttempts <= maxAttempts || *in.Change.MaxAttempts > 100000 {
				return out, fail("INVALID_BUDGET_INCREASE")
			}
			maxAttempts = *in.Change.MaxAttempts
		}
		if in.Change.MaxReplans != nil {
			if *in.Change.MaxReplans <= maxReplans || *in.Change.MaxReplans > 100 {
				return out, fail("INVALID_BUDGET_INCREASE")
			}
			maxReplans = *in.Change.MaxReplans
		}
		if _, err = q.ExecContext(ctx, "INSERT INTO goal_budget_policy(goal_id) VALUES(?) ON CONFLICT DO NOTHING", goalID); err != nil {
			return out, err
		}
		for _, v := range []struct {
			name  string
			value *int64
		}{{"max_tokens", in.Change.MaxTokens}, {"max_cost_units", in.Change.MaxCostUnits}} {
			if v.value == nil {
				continue
			}
			if *v.value < 1 || *v.value > 1000000000000 {
				return out, fail("INVALID_BUDGET_INCREASE")
			}
			var old sql.NullInt64
			if err = q.QueryRowContext(ctx, "SELECT "+v.name+" FROM goal_budget_policy WHERE goal_id=?", goalID).Scan(&old); err != nil {
				return out, err
			}
			if old.Valid && *v.value <= old.Int64 {
				return out, fail("INVALID_BUDGET_INCREASE")
			}
			if _, err = q.ExecContext(ctx, "UPDATE goal_budget_policy SET "+v.name+"=? WHERE goal_id=?", *v.value, goalID); err != nil {
				return out, err
			}
		}
	case "PROVIDE_EVIDENCE":
		if strings.TrimSpace(in.Change.Evidence) == "" {
			return out, fail("EVIDENCE_REQUIRED")
		}
	case "CHANGE_CONSTRAINT":
		if !has(actor.Scopes, "goals:constraints") || strings.TrimSpace(in.Change.Constraint) == "" {
			return out, fail("CONSTRAINT_CHANGE_UNAUTHORIZED")
		}
	case "REJECT":
		state = "FAILED"
	case "ABORT":
		state = "CANCELED"
	}
	out.Version = version + 1
	if _, err = q.ExecContext(ctx, "UPDATE goals SET state=?,version=?,updated=?,max_total_attempts=?,max_replans=? WHERE id=?", state, out.Version, store.Now(), maxAttempts, maxReplans, goalID); err != nil {
		return out, err
	}
	if _, err = q.ExecContext(ctx, "INSERT INTO goal_governance_decisions VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", goalID, in.OperationID, digest, actor.ID, in.Action, in.Reason, version, revision, generation, job.JSON(in.Change), out.Version, store.Now()); err != nil {
		return out, err
	}
	if in.Action == "APPROVE_NEXT_REPLAN" {
		_, err = q.ExecContext(ctx, "INSERT INTO goal_replan_permissions(goal_id,operation_id,plan_revision,graph_generation) VALUES(?,?,?,?)", goalID, in.OperationID, revision, generation)
	}
	return out, err
}

// ConsumeReplanPermissionTx consumes a human approval at most once. Call it in
// the same Server transaction that publishes the guarded Plan and Job; a
// rollback must restore the permission. It never launches work by itself.
func ConsumeReplanPermissionTx(ctx context.Context, q store.Query, goalID, operationID string, planRevision, graphGeneration int64) error {
	if goalID == "" || !job.ValidKey(operationID) || planRevision < 1 || graphGeneration < 1 {
		return fail("INVALID_REPLAN_PERMISSION")
	}
	result, err := q.ExecContext(ctx, `UPDATE goal_replan_permissions SET consumed=1
WHERE goal_id=? AND operation_id=? AND plan_revision=? AND graph_generation=? AND consumed=0
AND EXISTS (SELECT 1 FROM goals g WHERE g.id=goal_replan_permissions.goal_id
 AND g.active_plan_revision=goal_replan_permissions.plan_revision
 AND g.active_graph_generation=goal_replan_permissions.graph_generation
 AND g.state='NEEDS_APPROVAL')
AND EXISTS (SELECT 1 FROM goal_governance_decisions d
 WHERE d.goal_id=goal_replan_permissions.goal_id
 AND d.operation_id=goal_replan_permissions.operation_id
 AND d.action='APPROVE_NEXT_REPLAN')`, goalID, operationID, planRevision, graphGeneration)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fail("REPLAN_PERMISSION_UNAVAILABLE")
	}
	return nil
}

type Usage struct {
	ID, AttemptID, Source string
	Tokens, CostUnits     *int64
	Complete              bool
}

func RecordUsageTx(ctx context.Context, q store.Query, goalID string, u Usage) error {
	if !job.ValidKey(u.ID) || u.AttemptID == "" || strings.TrimSpace(u.Source) == "" || len(u.Source) > 128 || u.Complete && (u.Tokens == nil || u.CostUnits == nil) {
		return fail("INVALID_USAGE")
	}
	for _, v := range []*int64{u.Tokens, u.CostUnits} {
		if v != nil && (*v < 0 || *v > 2000000000000000) {
			return fail("INVALID_USAGE")
		}
	}
	digest := job.Hash(job.JSON(u))
	var old string
	err := q.QueryRowContext(ctx, "SELECT request_hash FROM goal_usage WHERE goal_id=? AND usage_id=?", goalID, u.ID).Scan(&old)
	if err == nil {
		if old != digest {
			return fail("USAGE_CONFLICT")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var n int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM goal_attempt_reservations WHERE goal_id=? AND attempt_id=?", goalID, u.AttemptID).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fail("USAGE_ATTEMPT_UNBOUND")
	}
	_, err = q.ExecContext(ctx, "INSERT INTO goal_usage VALUES(?,?,?,?,?,?,?,?,?)", goalID, u.ID, u.AttemptID, u.Source, u.Tokens, u.CostUnits, u.Complete, digest, store.Now())
	return err
}

// CheckUsageTx fails closed for unreported Attempts and incomplete usage. It is
// accounting admission, not a claim that an external runtime enforces hard caps.
func CheckUsageTx(ctx context.Context, q store.Query, goalID string) error {
	var tokens, cost sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT max_tokens,max_cost_units FROM goal_budget_policy WHERE goal_id=?", goalID).Scan(&tokens, &cost)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !tokens.Valid && !cost.Valid {
		return nil
	}
	var incomplete, usedTokens, usedCost int64
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM goal_attempt_reservations r LEFT JOIN goal_usage u ON u.attempt_id=r.attempt_id WHERE r.goal_id=? AND (u.usage_id IS NULL OR u.complete=0)`, goalID).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete > 0 {
		return fail("GOAL_USAGE_UNKNOWN")
	}
	if err = q.QueryRowContext(ctx, "SELECT coalesce(sum(tokens),0),coalesce(sum(cost_units),0) FROM goal_usage WHERE goal_id=?", goalID).Scan(&usedTokens, &usedCost); err != nil {
		return err
	}
	if tokens.Valid && usedTokens >= tokens.Int64 || cost.Valid && usedCost >= cost.Int64 {
		return fail("GOAL_USAGE_EXHAUSTED")
	}
	return nil
}

func DecodeRequest(raw []byte) (Request, error) {
	var in Request
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in, fmt.Errorf("invalid governance request: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return in, fail("INVALID_TRAILING_JSON")
	}
	return in, nil
}
