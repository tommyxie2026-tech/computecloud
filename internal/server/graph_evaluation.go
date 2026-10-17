package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GraphEvaluation struct {
	ExpectedValidityRevision int64  `json:"expected_validity_revision,omitempty"`
	ID                       string `json:"id"`
	ExpectedVersion          int64  `json:"expected_version"`
	Supersedes               string `json:"supersedes_evaluation_id,omitempty"`
	OutputHash               string `json:"output_sha256"`
	Verifier                 string `json:"verifier_profile"`
	VerifierVersion          string `json:"verifier_version"`
	Verdict                  string `json:"verdict"`
	EvidenceHash             string `json:"evidence_sha256"`
}

func (s *Server) EvaluateGraph(ctx context.Context, id string, in GraphEvaluation) error {
	if _, e := rpcutil.Require(ctx, "goals:evaluate", false); e != nil {
		return e
	}
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return e
	}
	if j.graph == nil {
		return status.Error(codes.InvalidArgument, "NOT_GRAPH_JOB")
	}
	if !job.ValidKey(in.ID) || !job.ValidHash(in.OutputHash) || !job.ValidHash(in.EvidenceHash) || in.ExpectedVersion < 0 || (in.Verdict != "PASS" && in.Verdict != "FAIL" && in.Verdict != "UNKNOWN") {
		return status.Error(codes.InvalidArgument, "GRAPH_EVALUATION_INVALID")
	}
	if in.Verifier != j.graph.Plan.Spec.Evaluator.Profile || in.VerifierVersion != j.graph.Plan.Spec.Evaluator.Version {
		return status.Error(codes.FailedPrecondition, "GRAPH_VERIFIER_MISMATCH")
	}
	return s.db.Tx(ctx, func(q store.Query) error {
		if _, e := graphMutationAuthorityTx(ctx, q, j, "goals:evaluate"); e != nil {
			return e
		}
		return applyGraphEvaluationTx(ctx, q, j.ID, in)
	})
}

func applyGraphEvaluationTx(ctx context.Context, q store.Query, jobID string, in GraphEvaluation) error {
	j, e := readJob(ctx, q, jobID)
	if e != nil {
		return e
	}
	f := j.graph
	if f == nil {
		return fmt.Errorf("NOT_GRAPH_JOB")
	}
	id := fmt.Sprintf("%s/%d/%s", f.GoalID, f.Generation, in.ID)
	body := job.JSON(in)
	var previous []byte
	e = q.QueryRowContext(ctx, `SELECT body FROM iu_evaluations WHERE id=? AND goal_id=? AND generation=?`, id, f.GoalID, f.Generation).Scan(&previous)
	if e == nil {
		if !bytes.Equal(previous, body) {
			return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	var generation, version, deadline int64
	var state, hash, current string
	var maxReplans, usedReplans int
	if e = q.QueryRowContext(ctx, `SELECT g.active_graph_generation,g.state,g.max_replans,g.consumed_replans,g.deadline,x.output_hash,x.evaluation_version,x.evaluation_id FROM goals g JOIN iu_graphs x ON x.goal_id=g.id WHERE x.job_id=?`, jobID).Scan(&generation, &state, &maxReplans, &usedReplans, &deadline, &hash, &version, &current); e != nil {
		return e
	}
	if deadline > 0 && deadline <= store.Now() || generation != f.Generation || state != "EVALUATING" && state != "REPLAN_GUARDING" || version != in.ExpectedVersion || in.Supersedes != current || in.OutputHash != hash || !terminal(j.State) {
		return status.Error(codes.Aborted, "GRAPH_EVALUATION_STALE")
	}
	validityRevision, validityErr := graphValidityRevisionTx(ctx, q, f)
	if validityErr != nil {
		return validityErr
	}
	if validityRevision != in.ExpectedValidityRevision {
		return status.Error(codes.Aborted, "GRAPH_VALIDITY_STALE")
	}
	invalidated, validityErr := graphInvalidatedTx(ctx, q, f)
	if validityErr != nil {
		return validityErr
	}
	if in.Verdict == "PASS" && invalidated {
		return status.Error(codes.FailedPrecondition, "IU_INPUT_INVALIDATED")
	}
	if in.Verdict == "PASS" && j.State != "SUCCEEDED" {
		return status.Error(codes.FailedPrecondition, "FAILED_EXECUTION_CANNOT_PASS")
	}
	if _, e = q.ExecContext(ctx, `INSERT INTO iu_evaluations(id,goal_id,generation,version,verdict,body,created) VALUES(?,?,?,?,?,?,?)`, id, f.GoalID, f.Generation, version+1, in.Verdict, body, store.Now()); e != nil {
		return e
	}
	if _, e = q.ExecContext(ctx, `UPDATE iu_graphs SET evaluation_id=?,evaluation_version=evaluation_version+1 WHERE job_id=? AND evaluation_version=?`, id, jobID, version); e != nil {
		return e
	}
	next := "EVALUATING"
	switch in.Verdict {
	case "PASS":
		next = "SUCCEEDED"
	case "FAIL":
		if maxReplans > usedReplans {
			next = "REPLAN_GUARDING"
		} else {
			next = "FAILED"
		}
	}
	if _, e = q.ExecContext(ctx, `UPDATE goals SET state=?,version=version+1,updated=? WHERE id=? AND active_graph_generation=?`, next, store.Now(), f.GoalID, f.Generation); e != nil {
		return e
	}
	return appendJobEvent(ctx, q, jobID, "graph.evaluated", map[string]any{"evaluation_id": id, "verdict": in.Verdict, "output_sha256": in.OutputHash, "evidence_sha256": in.EvidenceHash}, "", "")
}

func (s *Server) reconcileGraphEvaluations(ctx context.Context) error {
	rows, e := s.db.SQL.QueryContext(ctx, `SELECT j.id,x.output_hash,j.error_code FROM iu_graphs x JOIN jobs j ON j.id=x.job_id JOIN goals g ON g.id=x.goal_id WHERE j.state='FAILED' AND x.evaluation_version=0 AND g.active_graph_generation=x.generation AND g.state='EVALUATING' AND (g.deadline=0 OR g.deadline>?) LIMIT 16`, store.Now())
	if e != nil {
		return e
	}
	type failure struct{ id, hash, reason string }
	var failures []failure
	for rows.Next() {
		var f failure
		if e = rows.Scan(&f.id, &f.hash, &f.reason); e != nil {
			rows.Close()
			return e
		}
		failures = append(failures, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, f := range failures {
		if e = s.db.Tx(ctx, func(q store.Query) error {
			j, readErr := readJob(ctx, q, f.id)
			if readErr != nil {
				return readErr
			}
			revision, readErr := graphValidityRevisionTx(ctx, q, j.graph)
			if readErr != nil {
				return readErr
			}
			return applyGraphEvaluationTx(ctx, q, f.id, GraphEvaluation{ExpectedValidityRevision: revision, ID: "execution-failure", OutputHash: f.hash, Verifier: "computecloud-execution", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: job.Hash([]byte(f.reason))})
		}); e != nil {
			return e
		}
	}
	// Waiting evaluations have a durable Goal deadline even though their Job has ended.
	_, e = s.db.SQL.ExecContext(ctx, `UPDATE goals SET state='FAILED',version=version+1,updated=? WHERE state IN ('EVALUATING','REPLAN_GUARDING','NEEDS_APPROVAL') AND deadline>0 AND deadline<=? AND EXISTS(SELECT 1 FROM iu_graphs x WHERE x.goal_id=goals.id AND x.generation=goals.active_graph_generation)`, store.Now(), store.Now())
	return e
}

func (s *Server) httpEvaluateGraph(w http.ResponseWriter, r *http.Request) {
	raw, e := readJSONBody(w, r, 16<<10)
	if e != nil {
		httpError(w, e)
		return
	}
	var in GraphEvaluation
	if e = jsonutil.Decode(raw, &in); e != nil {
		httpError(w, status.Error(codes.InvalidArgument, "GRAPH_EVALUATION_INVALID"))
		return
	}
	if e = s.EvaluateGraph(r.Context(), r.PathValue("id"), in); e != nil {
		httpError(w, e)
		return
	}
	v, e := s.GraphStatus(r.Context(), r.PathValue("id"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, v)
}
