package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/telemetry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type traceContextKey struct{}

func withTrace(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceContextKey{}, id)
}
func traceFrom(ctx context.Context) string { v, _ := ctx.Value(traceContextKey{}).(string); return v }
func authorizeTrace(ctx context.Context, q store.Query, id, owner, project string) error {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM traces WHERE id=? AND owner=? AND project=?", id, owner, project).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return status.Error(codes.NotFound, "TRACE_NOT_FOUND")
	}
	return nil
}
func createTrace(ctx context.Context, q store.Query, id, owner, project, scope string) error {
	_, err := q.ExecContext(ctx, "INSERT INTO traces(id,owner,project,scope,created) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING", id, owner, project, scope, store.Now())
	return err
}
func (g *modelGateway) requestTrace(ctx context.Context, q store.Query, id modelIdentity, r *http.Request, f map[string]json.RawMessage) (string, error) {
	if id.job != "" {
		var trace sql.NullString
		err := q.QueryRowContext(ctx, "SELECT trace_id FROM jobs WHERE id=?", id.job).Scan(&trace)
		return trace.String, err
	}
	owner := id.traceOwner
	if owner == "" {
		owner = id.owner
	}
	if values := r.Header.Values("X-Computecloud-Trace-ID"); len(values) > 0 {
		if len(values) != 1 || !job.ValidKey(values[0]) {
			return "", status.Error(codes.InvalidArgument, "INVALID_TRACE_ID")
		}
		return values[0], authorizeTrace(ctx, q, values[0], owner, id.project)
	}
	var session string
	_ = json.Unmarshal(f["prompt_cache_key"], &session)
	scope := "request"
	trace := "trc_" + store.ID()
	if session != "" && len(session) <= 256 {
		scope = "conversation"
		trace = "trc_" + job.Hash(job.JSON([]string{"trace-v1", owner, id.project, id.route, session}))[:32]
	}
	return trace, createTrace(ctx, q, trace, owner, id.project, scope)
}
func saveAttemptMetrics(ctx context.Context, q store.Query, attempt string, raw []byte) error {
	var m telemetry.Attempt
	if len(raw) > 4096 || json.Unmarshal(raw, &m) != nil || (m.Source == "" || len(m.Source) > 128) {
		return status.Error(codes.InvalidArgument, "INVALID_ATTEMPT_METRICS")
	}
	var profile string
	if err := q.QueryRowContext(ctx, "SELECT json_extract(t.spec,'$.runtime_profile') FROM attempts a JOIN tasks t ON t.id=a.task WHERE a.id=?", attempt).Scan(&profile); err != nil {
		return err
	}
	if m.Source != profile {
		return status.Error(codes.InvalidArgument, "INVALID_ATTEMPT_METRICS")
	}
	if m.UsageComplete && m.Usage == nil {
		return status.Error(codes.InvalidArgument, "INVALID_ATTEMPT_METRICS")
	}
	if m.Usage != nil && (m.Usage.Input < 0 || m.Usage.Output < 0 || m.Usage.CachedInput < 0 || m.Usage.Input > 1e15 || m.Usage.Output > 1e15 || m.Usage.CachedInput > 1e15) {
		return status.Error(codes.InvalidArgument, "INVALID_ATTEMPT_METRICS")
	}
	if m.Process != nil && (m.Process.WallMS < 0 || m.Process.UserCPUMS < 0 || m.Process.SystemCPUMS < 0 || m.Process.PeakRSSBytes < 0) {
		return status.Error(codes.InvalidArgument, "INVALID_ATTEMPT_METRICS")
	}
	_, err := q.ExecContext(ctx, "INSERT INTO attempt_metrics(attempt_id,payload,recorded_at) VALUES(?,?,?)", attempt, job.JSON(m), store.Now())
	return err
}

type TraceAttempt struct {
	AttemptID  string             `json:"attempt_id"`
	TaskID     string             `json:"task_id"`
	WorkerID   string             `json:"worker_id"`
	Generation int64              `json:"generation"`
	Released   bool               `json:"released"`
	TaskState  string             `json:"task_state"`
	Metrics    *telemetry.Attempt `json:"metrics"`
	credential string
}

func (s *Server) traceAttempts(ctx context.Context, jid string) ([]TraceAttempt, error) {
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT a.id,t.id,a.worker,a.generation,a.released,t.state,m.payload,json_extract(t.spec,'$.credential_ref') FROM attempts a JOIN tasks t ON t.id=a.task LEFT JOIN attempt_metrics m ON m.attempt_id=a.id WHERE t.job_id=? ORDER BY t.id,a.generation`, jid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TraceAttempt{}
	for rows.Next() {
		var a TraceAttempt
		var raw []byte
		if err = rows.Scan(&a.AttemptID, &a.TaskID, &a.WorkerID, &a.Generation, &a.Released, &a.TaskState, &raw, &a.credential); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &a.Metrics); err != nil {
				return nil, err
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Job usage chooses one source per attempt, never adds native and gateway counters
// for the same inference. Retries remain separate measured attempts.
func (s *Server) jobUsage(ctx context.Context, j *Job) (map[string]any, error) {
	out := map[string]any{"coverage": "unavailable", "input_tokens": nil, "output_tokens": nil, "source": "per_attempt"}
	attempts, err := s.traceAttempts(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	var input, output int64
	known := 0
	for _, a := range attempts {
		if j.frozen.Routes[a.credential] != "" {
			var n, unknown int
			var in, ot sql.NullInt64
			if err = s.db.SQL.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(CASE WHEN usage_complete=0 THEN 1 ELSE 0 END),0),sum(input_tokens),sum(output_tokens) FROM gateway_requests WHERE attempt_id=?", a.AttemptID).Scan(&n, &unknown, &in, &ot); err != nil {
				return nil, err
			}
			if n > 0 && in.Valid && ot.Valid {
				input += in.Int64
				output += ot.Int64
				out["coverage"] = "partial"
				if unknown == 0 && a.Released {
					known++
				}
			}
		} else if a.Metrics != nil && a.Metrics.Usage != nil {
			input += a.Metrics.Usage.Input
			output += a.Metrics.Usage.Output
			out["coverage"] = "partial"
			if a.Metrics.NativeFinal && a.Metrics.UsageComplete && a.Released {
				known++
			}
		}
	}
	out["attempts"] = len(attempts)
	out["measured_attempts"] = known
	if out["coverage"] != "unavailable" {
		out["input_tokens"] = input
		out["output_tokens"] = output
	}
	if len(attempts) > 0 && known == len(attempts) && terminal(j.State) {
		out["coverage"] = "complete"
	}
	return out, nil
}

type TraceRequest struct {
	ID            string  `json:"request_id"`
	Owner         string  `json:"owner"`
	Model         string  `json:"model"`
	State         string  `json:"state"`
	JobID         *string `json:"job_id"`
	AttemptID     *string `json:"attempt_id"`
	Started       int64   `json:"started_at_ms,string"`
	Finished      *int64  `json:"finished_at_ms"`
	Input         *int64  `json:"input_tokens"`
	Output        *int64  `json:"output_tokens"`
	UsageComplete bool    `json:"usage_complete"`
	Error         string  `json:"error_code"`
}

func (s *Server) JobTrace(ctx context.Context, id, before string, limit int) (map[string]any, error) {
	j, err := s.jobAuthorized(ctx, id, "jobs:read")
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 1..100")
	}
	attempts, err := s.traceAttempts(ctx, id)
	if err != nil {
		return nil, dbErr(err)
	}
	out := map[string]any{"job_id": id, "trace_id": j.TraceID, "attempts": attempts, "model_requests": []TraceRequest{}, "has_more": false, "next_cursor": "", "trace_scope": "unavailable"}
	usage, err := s.jobUsage(ctx, j)
	if err != nil {
		return nil, dbErr(err)
	}
	out["worker_usage"] = usage
	if j.TraceID == "" {
		return out, nil
	}
	var scope string
	if err = s.db.SQL.QueryRowContext(ctx, "SELECT scope FROM traces WHERE id=? AND owner=? AND project=?", j.TraceID, j.owner, j.project).Scan(&scope); err != nil {
		return nil, dbErr(err)
	}
	out["trace_scope"] = scope
	var linked int
	if err = s.db.SQL.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE trace_id=?", j.TraceID).Scan(&linked); err != nil {
		return nil, dbErr(err)
	}
	out["linked_jobs"] = linked
	// Controller usage is shared conversation usage, not allocated to each Job.
	var n, unknown int
	var in, ot sql.NullInt64
	if err = s.db.SQL.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(CASE WHEN usage_complete=0 THEN 1 ELSE 0 END),0),sum(input_tokens),sum(output_tokens) FROM gateway_requests WHERE trace_id=? AND attempt_id IS NULL", j.TraceID).Scan(&n, &unknown, &in, &ot); err != nil {
		return nil, dbErr(err)
	}
	controller := map[string]any{"scope": scope, "requests": n, "unknown_requests": unknown, "input_tokens": nil, "output_tokens": nil}
	if in.Valid {
		controller["input_tokens"] = in.Int64
	}
	if ot.Valid {
		controller["output_tokens"] = ot.Int64
	}
	out["controller_usage"] = controller
	var beforeTime int64
	if before != "" {
		if err = s.db.SQL.QueryRowContext(ctx, "SELECT started FROM gateway_requests WHERE id=? AND trace_id=?", before, j.TraceID).Scan(&beforeTime); err != nil {
			return nil, status.Error(codes.InvalidArgument, "INVALID_CURSOR")
		}
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT id,owner,model,state,job_id,attempt_id,started,finished,input_tokens,output_tokens,usage_complete,error_code FROM gateway_requests WHERE trace_id=? AND (?='' OR started<? OR (started=? AND id<?)) ORDER BY started DESC,id DESC LIMIT ?`, j.TraceID, before, beforeTime, beforeTime, before, limit+1)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	list := []TraceRequest{}
	for rows.Next() {
		var v TraceRequest
		if err = rows.Scan(&v.ID, &v.Owner, &v.Model, &v.State, &v.JobID, &v.AttemptID, &v.Started, &v.Finished, &v.Input, &v.Output, &v.UsageComplete, &v.Error); err != nil {
			return nil, dbErr(err)
		}
		list = append(list, v)
	}
	if err = rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	if len(list) > limit {
		list = list[:limit]
		out["has_more"] = true
		out["next_cursor"] = list[len(list)-1].ID
	}
	out["model_requests"] = list
	return out, nil
}
func (s *Server) ListJobs(ctx context.Context, before string, limit int) (map[string]any, error) {
	p, err := rpcutil.Require(ctx, "jobs:read", false)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 1..100")
	}
	var beforeTime int64
	if before != "" {
		j, err := s.jobAuthorized(ctx, before, "jobs:read")
		if err != nil {
			return nil, err
		}
		beforeTime = j.Created
	}
	args := []any{p.Identity.Owner}
	marks := []string{}
	for _, project := range p.Identity.Projects {
		marks = append(marks, "?")
		args = append(args, project)
	}
	if len(marks) == 0 {
		return map[string]any{"jobs": []any{}, "has_more": false, "next_cursor": ""}, nil
	}
	args = append(args, before, beforeTime, beforeTime, before, limit+1)
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT id,state,project,created,updated,error_code,coalesce(trace_id,''),substr(json_extract(spec,'$.spec.input.text'),1,300) FROM jobs WHERE owner=? AND project IN (`+strings.Join(marks, ",")+`) AND (?='' OR created<? OR (created=? AND id<?)) ORDER BY created DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, state, project, code, trace string
		var summary sql.NullString
		var created, updated int64
		if err = rows.Scan(&id, &state, &project, &created, &updated, &code, &trace, &summary); err != nil {
			return nil, dbErr(err)
		}
		list = append(list, map[string]any{"job_id": id, "state": state, "project_id": project, "created_at_ms": created, "updated_at_ms": updated, "error_code": code, "trace_id": trace, "input_summary": summary.String})
	}
	if err = rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	more := len(list) > limit
	cursor := ""
	if more {
		list = list[:limit]
		cursor = list[len(list)-1]["job_id"].(string)
	}
	return map[string]any{"jobs": list, "has_more": more, "next_cursor": cursor}, nil
}
