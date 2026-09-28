package goal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type Budget struct {
	MaxReplans       int
	MaxTotalAttempts int
	MaxWallTime      time.Duration
	DeadlineMS       int64
}

type CreateGoal struct {
	ID      string
	Owner   string
	Project string
	Budget  Budget
}

type ReplanRequest struct {
	ID                      string
	GoalID                  string
	EvaluationID            string
	ExpectedPlanRevision    int64
	ExpectedGraphGeneration int64
	ReasonCode              string
	Evidence                ReplanEvidence
	ProposedPlan            PlanCanonical
}

type ReplanDecision struct {
	Allowed             bool
	NeedsApproval       bool
	Code                string
	NextPlanRevision    int64
	NextGraphGeneration int64
	Existing            bool
}

const (
	DecisionAllowed          = "ALLOWED"
	DecisionBudgetExhausted  = "BUDGET_EXHAUSTED"
	DecisionAttemptExhausted = "ATTEMPT_BUDGET_EXHAUSTED"
	DecisionWallTimeExceeded = "WALL_TIME_EXCEEDED"
	DecisionDeadlineExceeded = "DEADLINE_EXCEEDED"
	DecisionStaleGeneration  = "STALE_GENERATION"
	DecisionGoalTerminal     = "GOAL_TERMINAL"
	DecisionNoNewEvidence    = "NO_NEW_EVIDENCE"
	DecisionDuplicatePlan    = "DUPLICATE_PLAN"
)

func Create(ctx context.Context, db *store.DB, in CreateGoal) error {
	if in.ID == "" || in.Owner == "" || in.Project == "" {
		return fmt.Errorf("goal id, owner and project are required")
	}
	if in.Budget.MaxReplans < 0 || in.Budget.MaxTotalAttempts < 1 || in.Budget.MaxWallTime <= 0 {
		return fmt.Errorf("invalid goal budget")
	}
	now := store.Now()
	_, err := db.SQL.ExecContext(ctx,
		"INSERT INTO goals(id,owner,project,state,active_plan_revision,active_graph_generation,max_replans,max_total_attempts,max_wall_time_ms,created,updated,deadline) VALUES(?,?,?,'GOAL_CREATED',1,1,?,?,?,?,?,?)",
		in.ID, in.Owner, in.Project,
		in.Budget.MaxReplans, in.Budget.MaxTotalAttempts, in.Budget.MaxWallTime.Milliseconds(),
		now, now, in.Budget.DeadlineMS)
	return err
}

func ReserveAttempt(ctx context.Context, db *store.DB, goalID string) error {
	return db.Tx(ctx, func(q store.Query) error {
		var max, used int
		var state string
		if err := q.QueryRowContext(ctx,
			"SELECT state,max_total_attempts,consumed_attempts FROM goals WHERE id=?",
			goalID).Scan(&state, &max, &used); err != nil {
			return err
		}
		if terminal(state) {
			return errors.New(DecisionGoalTerminal)
		}
		if used >= max {
			return errors.New(DecisionAttemptExhausted)
		}
		res, err := q.ExecContext(ctx,
			"UPDATE goals SET consumed_attempts=consumed_attempts+1,updated=?,version=version+1 WHERE id=? AND consumed_attempts<?",
			store.Now(), goalID, max)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New(DecisionAttemptExhausted)
		}
		return nil
	})
}

func RegisterPlanFingerprint(ctx context.Context, db *store.DB, goalID string, revision int64, plan PlanCanonical) error {
	if goalID == "" || revision < 1 {
		return fmt.Errorf("goal id and positive plan revision are required")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	_, err := db.SQL.ExecContext(ctx,
		"INSERT INTO plan_fingerprints(goal_id,plan_revision,fingerprint,created) VALUES(?,?,?,?)",
		goalID, revision, plan.Fingerprint(), store.Now())
	return err
}

func GuardReplan(ctx context.Context, db *store.DB, in ReplanRequest) (ReplanDecision, error) {
	if in.ID == "" || in.GoalID == "" || in.EvaluationID == "" {
		return ReplanDecision{}, fmt.Errorf("replan request id, goal id and evaluation id are required")
	}
	if err := in.Evidence.Validate(); err != nil {
		return ReplanDecision{}, err
	}
	if err := in.ProposedPlan.Validate(); err != nil {
		return ReplanDecision{}, err
	}
	evidenceFingerprint := in.Evidence.Fingerprint()
	planFingerprint := in.ProposedPlan.Fingerprint()

	var out ReplanDecision
	err := db.Tx(ctx, func(q store.Query) error {
		var existingState, existingCode, existingEvidence, existingPlanFingerprint string
		var existingPlan, existingGraph int64
		err := q.QueryRowContext(ctx,
			"SELECT state,decision_code,next_plan_revision,next_graph_generation,evidence_fingerprint,proposed_plan_fingerprint FROM replan_requests WHERE goal_id=? AND evaluation_id=?",
			in.GoalID, in.EvaluationID).Scan(&existingState, &existingCode, &existingPlan, &existingGraph, &existingEvidence, &existingPlanFingerprint)
		if err == nil {
			if existingEvidence != evidenceFingerprint || existingPlanFingerprint != planFingerprint {
				return fmt.Errorf("IDEMPOTENCY_CONFLICT")
			}
			out.Existing = true
			out.Allowed = existingState == DecisionAllowed
			out.NeedsApproval = existingState == "NEEDS_APPROVAL"
			out.Code = existingCode
			out.NextPlanRevision = existingPlan
			out.NextGraphGeneration = existingGraph
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var state string
		var activePlan, activeGraph int64
		var maxReplans, maxAttempts, usedReplans, usedAttempts int
		var maxWallMS, created, deadline int64
		if err = q.QueryRowContext(ctx,
			"SELECT state,active_plan_revision,active_graph_generation,max_replans,max_total_attempts,consumed_replans,consumed_attempts,max_wall_time_ms,created,deadline FROM goals WHERE id=?",
			in.GoalID).Scan(&state, &activePlan, &activeGraph, &maxReplans, &maxAttempts,
			&usedReplans, &usedAttempts, &maxWallMS, &created, &deadline); err != nil {
			return err
		}

		now := store.Now()
		requestState := DecisionAllowed
		code := DecisionAllowed
		nextPlan, nextGraph := activePlan+1, activeGraph+1

		var seenEvidence, seenPlan int
		if err = q.QueryRowContext(ctx,
			"SELECT count(*) FROM goal_evidence WHERE goal_id=? AND fingerprint=?",
			in.GoalID, evidenceFingerprint).Scan(&seenEvidence); err != nil {
			return err
		}
		if err = q.QueryRowContext(ctx,
			"SELECT count(*) FROM plan_fingerprints WHERE goal_id=? AND fingerprint=?",
			in.GoalID, planFingerprint).Scan(&seenPlan); err != nil {
			return err
		}

		switch {
		case terminal(state):
			requestState, code = "REJECTED", DecisionGoalTerminal
			nextPlan, nextGraph = 0, 0
		case activePlan != in.ExpectedPlanRevision || activeGraph != in.ExpectedGraphGeneration:
			requestState, code = "REJECTED", DecisionStaleGeneration
			nextPlan, nextGraph = 0, 0
		case usedAttempts >= maxAttempts:
			requestState, code = "NEEDS_APPROVAL", DecisionAttemptExhausted
			nextPlan, nextGraph = 0, 0
		case usedReplans >= maxReplans:
			requestState, code = "NEEDS_APPROVAL", DecisionBudgetExhausted
			nextPlan, nextGraph = 0, 0
		case maxWallMS > 0 && now-created >= maxWallMS:
			requestState, code = "NEEDS_APPROVAL", DecisionWallTimeExceeded
			nextPlan, nextGraph = 0, 0
		case deadline > 0 && now >= deadline:
			requestState, code = "NEEDS_APPROVAL", DecisionDeadlineExceeded
			nextPlan, nextGraph = 0, 0
		case seenEvidence != 0:
			requestState, code = "NEEDS_APPROVAL", DecisionNoNewEvidence
			nextPlan, nextGraph = 0, 0
		case seenPlan != 0:
			requestState, code = "NEEDS_APPROVAL", DecisionDuplicatePlan
			nextPlan, nextGraph = 0, 0
		}

		if _, err = q.ExecContext(ctx,
			"INSERT INTO replan_requests(id,goal_id,evaluation_id,expected_plan_revision,expected_graph_generation,reason_code,state,decision_code,next_plan_revision,next_graph_generation,created,decided,failure_class,evidence_fingerprint,proposed_plan_fingerprint) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			in.ID, in.GoalID, in.EvaluationID, in.ExpectedPlanRevision, in.ExpectedGraphGeneration,
			in.ReasonCode, requestState, code, nextPlan, nextGraph, now, now,
			string(in.Evidence.FailureClass), evidenceFingerprint, planFingerprint); err != nil {
			return err
		}

		if requestState == DecisionAllowed {
			if _, err = q.ExecContext(ctx,
				"INSERT INTO goal_evidence(goal_id,fingerprint,evaluation_id,failure_class,created) VALUES(?,?,?,?,?)",
				in.GoalID, evidenceFingerprint, in.EvaluationID, string(in.Evidence.FailureClass), now); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx,
				"INSERT INTO plan_fingerprints(goal_id,plan_revision,fingerprint,created) VALUES(?,?,?,?)",
				in.GoalID, nextPlan, planFingerprint, now); err != nil {
				return err
			}
			res, err := q.ExecContext(ctx,
				"UPDATE goals SET state='REPLANNING',consumed_replans=consumed_replans+1,active_plan_revision=?,active_graph_generation=?,updated=?,version=version+1 WHERE id=? AND active_plan_revision=? AND active_graph_generation=? AND consumed_replans<? AND consumed_attempts<? AND state NOT IN ('SUCCEEDED','FAILED','CANCELED')",
				nextPlan, nextGraph, now, in.GoalID,
				in.ExpectedPlanRevision, in.ExpectedGraphGeneration,
				maxReplans, maxAttempts)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("replan reservation lost generation race")
			}
		} else if requestState == "NEEDS_APPROVAL" {
			if _, err = q.ExecContext(ctx,
				"UPDATE goals SET state='NEEDS_APPROVAL',updated=?,version=version+1 WHERE id=? AND state NOT IN ('SUCCEEDED','FAILED','CANCELED')",
				now, in.GoalID); err != nil {
				return err
			}
		}

		out.Allowed = requestState == DecisionAllowed
		out.NeedsApproval = requestState == "NEEDS_APPROVAL"
		out.Code = code
		out.NextPlanRevision = nextPlan
		out.NextGraphGeneration = nextGraph
		return nil
	})
	return out, err
}

func terminal(state string) bool {
	return state == "SUCCEEDED" || state == "FAILED" || state == "CANCELED"
}
