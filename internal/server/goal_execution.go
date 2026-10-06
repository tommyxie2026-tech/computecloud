package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type goalGraphNode struct {
	ID        string   `json:"id"`
	Stage     string   `json:"stage"`
	DependsOn []string `json:"depends_on"`
}

func compileJobGraph(spec job.Spec) []goalGraphNode {
	if spec.Map == nil {
		return []goalGraphNode{{"single", "single", []string{}}}
	}
	nodes := make([]goalGraphNode, 0, len(spec.Map.Partitions)+1)
	deps := []string{}
	for _, part := range spec.Map.Partitions {
		id := "map:" + part.Key
		nodes = append(nodes, goalGraphNode{id, "map", []string{}})
		deps = append(deps, id)
	}
	return append(nodes, goalGraphNode{"reduce", "reduce", deps})
}

// Adoption and creation run in the caller's Job/Attempt transaction.
func ensureGoalBinding(ctx context.Context, q store.Query, j *Job) (string, error) {
	var goalID string
	err := q.QueryRowContext(ctx, "SELECT goal_id FROM goal_job_bindings WHERE job_id=?", j.ID).Scan(&goalID)
	if err == nil {
		return goalID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	goalID = "goal_" + j.ID
	graph := compileJobGraph(j.frozen.Spec)
	maxPerTask := j.frozen.Spec.Limits.MaxAttemptsPerTask
	if maxPerTask < 1 {
		maxPerTask = 1
	}
	maximum := len(graph) * maxPerTask
	var used int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM attempts a JOIN tasks t ON a.task=t.id WHERE t.job_id=?", j.ID).Scan(&used); err != nil {
		return "", err
	}
	if used > maximum {
		maximum = used
	}
	wall := j.Deadline - j.Created
	if wall < 1 {
		wall = 1
	}
	now := store.Now()
	if _, err = q.ExecContext(ctx, `INSERT INTO goals(id,owner,project,state,max_replans,max_total_attempts,max_wall_time_ms,consumed_attempts,created,updated,deadline) VALUES(?,?,?,'RUNNING',0,?,?,?,?,?,?)`, goalID, j.owner, j.project, maximum, wall, used, j.Created, now, j.Deadline); err != nil {
		return "", err
	}
	if _, err = q.ExecContext(ctx, "INSERT INTO goal_job_bindings VALUES(?,?,1,1)", j.ID, goalID); err != nil {
		return "", err
	}
	if _, err = q.ExecContext(ctx, "INSERT INTO goal_plans VALUES(?,1,1,?,?,?,?)", goalID, j.ID, job.JSON(j.frozen), job.JSON(graph), now); err != nil {
		return "", err
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO goal_attempt_reservations SELECT a.id,?,1,1,? FROM attempts a JOIN tasks t ON a.task=t.id WHERE t.job_id=?`, goalID, now, j.ID); err != nil {
		return "", err
	}
	return goalID, nil
}

func reserveGoalAttempt(ctx context.Context, q store.Query, j *Job, a *pb.Assignment) error {
	if j == nil {
		return nil
	}
	goalID, err := ensureGoalBinding(ctx, q, j)
	if err != nil {
		return err
	}
	// The Attempt must be inserted in this same transaction before the FK reservation.
	var exists int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM goal_attempt_reservations WHERE attempt_id=?", a.AttemptId).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	result, err := q.ExecContext(ctx, `UPDATE goals SET consumed_attempts=consumed_attempts+1,version=version+1,updated=?
 WHERE id=? AND state IN ('RUNNING','GRAPH_READY','REPLANNING') AND consumed_attempts<max_total_attempts
 AND (deadline=0 OR deadline>?) AND created+max_wall_time_ms>?
 AND EXISTS(SELECT 1 FROM goal_job_bindings b WHERE b.job_id=? AND b.goal_id=goals.id AND b.plan_revision=goals.active_plan_revision AND b.graph_generation=goals.active_graph_generation)`, store.Now(), goalID, store.Now(), store.Now(), j.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("GOAL_EXECUTION_FENCED")
	}
	_, err = q.ExecContext(ctx, `INSERT INTO goal_attempt_reservations SELECT ?,goal_id,plan_revision,graph_generation,? FROM goal_job_bindings WHERE job_id=?`, a.AttemptId, store.Now(), j.ID)
	return err
}

type goalArtifactEvidence struct {
	ID         string `json:"artifact_id"`
	SHA256     string `json:"sha256"`
	AttemptID  string `json:"attempt_id"`
	Generation int64  `json:"generation"`
}

func evaluateBoundJob(ctx context.Context, q store.Query, j *Job) error {
	if !terminal(j.State) {
		return nil
	}
	goalID, err := ensureGoalBinding(ctx, q, j)
	if err != nil {
		return err
	}
	missingRows, err := q.QueryContext(ctx, `SELECT r.attempt_id FROM goal_attempt_reservations r LEFT JOIN goal_usage u ON u.attempt_id=r.attempt_id WHERE r.goal_id=? AND u.usage_id IS NULL`, goalID)
	if err != nil {
		return err
	}
	missing := []string{}
	for missingRows.Next() {
		var id string
		if err = missingRows.Scan(&id); err != nil {
			missingRows.Close()
			return err
		}
		missing = append(missing, id)
	}
	err = missingRows.Err()
	missingRows.Close()
	if err != nil {
		return err
	}
	for _, id := range missing {
		if err = recordGoalAttemptUsage(ctx, q, j, goalID, id); err != nil {
			return err
		}
	}
	var revision, generation int64
	if err = q.QueryRowContext(ctx, `SELECT b.plan_revision,b.graph_generation FROM goal_job_bindings b JOIN goals g ON g.id=b.goal_id
 WHERE b.job_id=? AND b.plan_revision=g.active_plan_revision AND b.graph_generation=g.active_graph_generation`, j.ID).Scan(&revision, &generation); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var present int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM goal_evaluations WHERE goal_id=? AND graph_generation=? AND job_version=?", goalID, generation, j.Version).Scan(&present); err != nil {
		return err
	}
	if present != 0 {
		return nil
	}
	evidence := []goalArtifactEvidence{}
	rows, err := q.QueryContext(ctx, `SELECT a.id,a.hash,a.attempt,a.generation FROM artifacts a JOIN artifact_refs r ON r.artifact=a.id
 JOIN tasks t ON t.id=a.task JOIN attempts x ON x.id=a.attempt
 WHERE r.ref_type='job_result' AND r.ref_id=? AND a.state='ACCEPTED' AND t.job_id=?
 AND t.attempt=a.attempt AND t.current_generation=a.generation AND x.generation=a.generation AND x.released=1 ORDER BY a.id`, j.ID, j.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e goalArtifactEvidence
		if err = rows.Scan(&e.ID, &e.SHA256, &e.AttemptID, &e.Generation); err != nil {
			rows.Close()
			return err
		}
		evidence = append(evidence, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	verdict := j.State
	reason := j.ErrorCode
	if verdict == "SUCCEEDED" && len(evidence) == 0 {
		verdict = "FAILED"
		reason = "ARTIFACT_PROVENANCE_UNVERIFIED"
	}
	body := job.JSON(map[string]any{"job_id": j.ID, "job_state": j.State, "reason": reason, "artifacts": evidence, "plan_revision": revision})
	if _, err = q.ExecContext(ctx, "INSERT INTO goal_evaluations VALUES(?,?,?,?,'job-artifact-v1',?,?,?)", goalID, generation, j.ID, j.Version, verdict, body, store.Now()); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE goals SET state=?,version=version+1,updated=? WHERE id=? AND active_plan_revision=? AND active_graph_generation=? AND state NOT IN ('SUCCEEDED','FAILED','CANCELED')`, verdict, store.Now(), goalID, revision, generation)
	return err
}

// Reconcile at most 16 historical/unmapped Jobs per tick. No Job is replayed.
func (s *Server) reconcileGoalBindings(ctx context.Context) error {
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT j.id FROM jobs j LEFT JOIN goal_job_bindings b ON b.job_id=j.id WHERE b.job_id IS NULL ORDER BY j.created,j.id LIMIT 16`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.db.Tx(ctx, func(q store.Query) error {
			j, e := readJob(ctx, q, id)
			if e != nil {
				return e
			}
			if _, e = ensureGoalBinding(ctx, q, j); e != nil {
				return e
			}
			return evaluateBoundJob(ctx, q, j)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) httpJobGoal(w http.ResponseWriter, r *http.Request) {
	j, err := s.jobAuthorized(r.Context(), r.PathValue("id"), "jobs:read")
	if err != nil {
		httpError(w, err)
		return
	}
	var id, state string
	var revision, generation, used, maximum, version int64
	var graph []byte
	err = s.db.Tx(r.Context(), func(q store.Query) error {
		var e error
		j, e = readJob(r.Context(), q, j.ID)
		if e != nil {
			return e
		}
		id, e = ensureGoalBinding(r.Context(), q, j)
		if e != nil {
			return e
		}
		if e = evaluateBoundJob(r.Context(), q, j); e != nil {
			return e
		}
		return q.QueryRowContext(r.Context(), `SELECT g.state,g.active_plan_revision,g.active_graph_generation,g.consumed_attempts,g.max_total_attempts,p.graph_json,g.version FROM goals g JOIN goal_plans p ON p.goal_id=g.id AND p.revision=g.active_plan_revision WHERE g.id=?`, id).Scan(&state, &revision, &generation, &used, &maximum, &graph, &version)
	})
	if err != nil {
		httpError(w, dbErr(err))
		return
	}
	governanceState, e := goalGovernanceProjection(r.Context(), s.db.SQL, id)
	if e != nil {
		httpError(w, dbErr(e))
		return
	}
	jsonResponse(w, 200, map[string]any{"governance": governanceState, "goal_id": id, "state": state, "version": version, "plan_revision": revision, "graph_generation": generation, "consumed_attempts": used, "max_total_attempts": maximum, "graph": json.RawMessage(graph), "synthetic": true, "automatic_replan_enabled": false})
}
