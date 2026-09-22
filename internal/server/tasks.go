package server

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func encode(m proto.Message) []byte {
	b, e := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if e != nil {
		panic(e)
	}
	return b
}
func decode(b []byte, m proto.Message) error { return protojson.Unmarshal(b, m) }
func terminal(s string) bool                 { return s == "SUCCEEDED" || s == "FAILED" || s == "CANCELED" }
func dbErr(e error) error {
	if e == nil {
		return nil
	}
	if _, ok := status.FromError(e); ok {
		return e
	}
	if errors.Is(e, sql.ErrNoRows) {
		return status.Error(codes.NotFound, "not found")
	}
	return status.Error(codes.Unavailable, "storage operation failed")
}
func readTask(ctx context.Context, q store.Query, id string) (*pb.Task, string, error) {
	t := new(pb.Task)
	var b []byte
	var owner string
	e := q.QueryRowContext(ctx, `SELECT id,owner,state,attempt,spec,seq,error_code,error_message,result,native_session,worker,created,updated,blocker FROM tasks WHERE id=?`, id).Scan(&t.TaskId, &owner, &t.State, &t.AttemptId, &b, &t.LastSeq, &t.ErrorCode, &t.ErrorMessage, &t.Result, &t.SessionRef, &t.WorkerId, &t.CreatedAtMs, &t.UpdatedAtMs, &t.SchedulingBlocker)
	if e != nil {
		return nil, "", e
	}
	t.Spec = new(pb.TaskSpec)
	e = decode(b, t.Spec)
	return t, owner, e
}
func (s *Server) authorized(ctx context.Context, id string) (*pb.Task, error) {
	t, e := s.authorizedOwner(ctx, id)
	if e != nil {
		return nil, e
	}
	if e = taskJobReadScope(ctx, s.db.SQL, id); e != nil {
		return nil, e
	}
	return t, nil
}
func (s *Server) authorizedOwner(ctx context.Context, id string) (*pb.Task, error) {
	p, e := rpcutil.User(ctx)
	if e != nil {
		return nil, e
	}
	t, owner, e := readTask(ctx, s.db.SQL, id)
	if e != nil {
		return nil, dbErr(e)
	}
	if owner != p.Identity.Owner || !config.Contains(p.Identity.Projects, t.Spec.ProjectId) {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return t, nil
}

var commitRE = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

func (s *Server) SubmitTask(ctx context.Context, in *pb.TaskSpec) (*pb.Task, error) {
	p, e := rpcutil.Require(ctx, "tasks:submit", true)
	if e != nil {
		return nil, e
	}
	if s.cfg.Maintenance {
		return nil, status.Error(codes.Unavailable, "maintenance mode")
	}
	spec := proto.Clone(in).(*pb.TaskSpec)
	if strings.HasPrefix(spec.IdempotencyKey, "__job/") {
		return nil, status.Error(codes.InvalidArgument, "reserved idempotency prefix")
	}
	if !config.Contains(p.Identity.Projects, spec.ProjectId) || !config.Contains(p.Identity.Credentials, spec.CredentialRef) {
		return nil, status.Error(codes.PermissionDenied, "project or credential not allowed")
	}
	if spec.IdempotencyKey == "" || len(spec.IdempotencyKey) > 200 || spec.Input == nil || len(spec.Input.Text) == 0 || len(spec.Input.Text) > 1<<20 || spec.Workspace == nil || spec.Workspace.RepositoryRef == "" || !commitRE.MatchString(spec.Workspace.BaseCommit) {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key, input and repository with fixed commit required")
	}
	if spec.RuntimeProfile == "" {
		spec.RuntimeProfile = map[string]string{"codex": "codex_exec", "claude": "claude_print"}[spec.Engine]
	}
	if !((spec.Engine == "codex" && spec.RuntimeProfile == "codex_exec") || (spec.Engine == "claude" && spec.RuntimeProfile == "claude_print")) {
		return nil, status.Error(codes.FailedPrecondition, "unsupported runtime")
	}
	if spec.SessionRef != "" || spec.ProviderRef != "" {
		return nil, status.Error(codes.FailedPrecondition, "session resume and provider override not enabled in v0.1")
	}
	for _, c := range spec.RequiredCapabilities {
		if c != "event_stream" && c != "cancel" {
			return nil, status.Error(codes.FailedPrecondition, "unsupported capability: "+c)
		}
	}
	if spec.Workspace.IsolationProfile == "" {
		spec.Workspace.IsolationProfile = "trusted-worktree-process"
	}
	if spec.Workspace.IsolationProfile != "trusted-worktree-process" {
		return nil, status.Error(codes.FailedPrecondition, "only trusted process execution implemented")
	}
	if spec.PolicyRef == "" || spec.AcceptanceProfile == "" {
		return nil, status.Error(codes.InvalidArgument, "policy_ref and acceptance_profile required")
	}
	if spec.Model == "" {
		spec.Model = s.cfg.Models[spec.RuntimeProfile]
	}
	if spec.Model == "" || strings.HasPrefix(spec.Model, "-") {
		return nil, status.Error(codes.InvalidArgument, "explicit model or configured model default required")
	}
	if s.cfg.Credentials[spec.CredentialRef] < 1 {
		return nil, status.Error(codes.InvalidArgument, "unknown credential")
	}
	if spec.TimeoutSeconds == 0 {
		spec.TimeoutSeconds = 1800
	}
	if spec.TimeoutSeconds < 1 || spec.TimeoutSeconds > 86400 || spec.Priority < 0 || spec.Priority > 10 {
		return nil, status.Error(codes.InvalidArgument, "invalid timeout or priority")
	}
	raw := encode(spec)
	hash := store.Hash(raw)
	id := store.ID()
	existing := false
	e = s.db.Tx(ctx, func(q store.Query) error {
		var oldHash string
		e := q.QueryRowContext(ctx, "SELECT id,hash FROM tasks WHERE owner=? AND project=? AND idem=?", p.Identity.Owner, spec.ProjectId, spec.IdempotencyKey).Scan(&id, &oldHash)
		if e == nil {
			if oldHash != hash {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			existing = true
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		now := store.Now()
		_, e = q.ExecContext(ctx, `INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,priority,deadline) VALUES(?,?,?,?,?,?,'QUEUED',?,?,?,?)`, id, p.Identity.Owner, spec.ProjectId, spec.IdempotencyKey, hash, raw, now, now, spec.Priority, now+spec.TimeoutSeconds*1000)
		if e != nil {
			return e
		}
		return appendEvent(ctx, q, &pb.Event{TaskId: id, Type: "task.state_changed", PayloadJson: config.JSON(map[string]string{"to": "QUEUED"})}, nil)
	})
	if e != nil {
		return nil, dbErr(e)
	}
	s.wake()
	t, _, e := readTask(ctx, s.db.SQL, id)
	if e != nil {
		return nil, dbErr(e)
	}
	t.Existing = existing
	return t, nil
}
func appendEvent(ctx context.Context, q store.Query, e *pb.Event, workerSeq *int64) error {
	rawHash := store.Hash(encode(e))
	e.EventId = choose(e.EventId, store.ID())
	e.RecordedAtMs = store.Now()
	if err := q.QueryRowContext(ctx, "UPDATE tasks SET seq=seq+1,updated=? WHERE id=? RETURNING seq", e.RecordedAtMs, e.TaskId).Scan(&e.Seq); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "INSERT INTO events(task,seq,attempt,worker_seq,id,hash,body) VALUES(?,?,?,?,?,?,?)", e.TaskId, e.Seq, e.AttemptId, workerSeq, e.EventId, rawHash, encode(e))
	return err
}
func choose(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func (s *Server) GetTask(ctx context.Context, r *pb.TaskRef) (*pb.Task, error) {
	return s.authorized(ctx, r.TaskId)
}
func (s *Server) CancelTask(ctx context.Context, r *pb.CancelRequest) (*pb.Task, error) {
	if _, e := rpcutil.Require(ctx, "tasks:cancel", true); e != nil {
		return nil, e
	}
	if r.ControlId == "" || len(r.Reason) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "control_id required and reason must be bounded")
	}
	if _, e := s.authorizedOwner(ctx, r.TaskId); e != nil {
		return nil, e
	}
	var managed int
	if e := s.db.SQL.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE id=? AND job_id IS NOT NULL", r.TaskId).Scan(&managed); e != nil {
		return nil, dbErr(e)
	}
	if managed != 0 {
		return nil, status.Error(codes.FailedPrecondition, "MANAGED_JOB_TASK")
	}
	e := s.db.Tx(ctx, func(q store.Query) error {
		hash := store.Hash(encode(r))
		var old string
		e := q.QueryRowContext(ctx, "SELECT hash FROM controls WHERE task=? AND id=?", r.TaskId, r.ControlId).Scan(&old)
		if e == nil {
			if old != hash {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = q.ExecContext(ctx, "INSERT INTO controls VALUES(?,?,?)", r.TaskId, r.ControlId, hash); e != nil {
			return e
		}
		t, _, e := readTask(ctx, q, r.TaskId)
		if e != nil {
			return e
		}
		if terminal(t.State) {
			return nil
		}
		if t.AttemptId == "" {
			return setState(ctx, q, t.TaskId, "CANCELED", "", "")
		}
		if e = setState(ctx, q, t.TaskId, "CANCELING", "", ""); e != nil {
			return e
		}
		return s.stopCommand(ctx, q, t)
	})
	s.wake()
	if e != nil {
		return nil, dbErr(e)
	}
	return s.authorizedOwner(ctx, r.TaskId)
}
func setState(ctx context.Context, q store.Query, id, state, code, msg string) error {
	var old string
	if e := q.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id=?", id).Scan(&old); e != nil {
		return e
	}
	if old == state {
		return nil
	}
	if terminal(old) {
		return nil
	}
	if _, e := q.ExecContext(ctx, "UPDATE tasks SET state=?,error_code=?,error_message=?,updated=? WHERE id=?", state, code, msg, store.Now(), id); e != nil {
		return e
	}
	typ := "task.state_changed"
	if terminal(state) {
		typ = "task.completed"
	}
	if e := appendEvent(ctx, q, &pb.Event{TaskId: id, Type: typ, PayloadJson: config.JSON(map[string]string{"from": old, "to": state, "code": code, "message": msg})}, nil); e != nil {
		return e
	}
	var jid sql.NullString
	if e := q.QueryRowContext(ctx, "SELECT job_id FROM tasks WHERE id=?", id).Scan(&jid); e != nil {
		return e
	}
	if jid.Valid {
		return appendJobEvent(ctx, q, jid.String, "job.task_state", map[string]string{"task_id": id, "from": old, "to": state, "code": code}, "", "")
	}
	return nil
}
func (s *Server) WatchEvents(r *pb.WatchRequest, stream grpc.ServerStreamingServer[pb.Event]) error {
	t, e := s.authorized(stream.Context(), r.TaskId)
	if e != nil {
		return e
	}
	if r.AfterSeq < 0 || r.AfterSeq > t.LastSeq {
		return status.Error(codes.OutOfRange, "invalid event cursor")
	}
	seq := r.AfterSeq
	for {
		rows, e := s.db.SQL.QueryContext(stream.Context(), "SELECT body FROM events WHERE task=? AND seq>? ORDER BY seq LIMIT 100", r.TaskId, seq)
		if e != nil {
			return dbErr(e)
		}
		var events []*pb.Event
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				break
			}
			v := new(pb.Event)
			if e = decode(b, v); e != nil {
				break
			}
			events = append(events, v)
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return dbErr(e)
		}
		if re != nil {
			return dbErr(re)
		}
		for _, v := range events {
			if e = stream.Send(v); e != nil {
				return e
			}
			seq = v.Seq
		}
		t, _, e = readTask(stream.Context(), s.db.SQL, r.TaskId)
		if e != nil {
			return dbErr(e)
		}
		if terminal(t.State) && seq >= t.LastSeq {
			return nil
		}
		if e = wait(stream.Context(), 100); e != nil {
			return e
		}
	}
}
func (s *Server) SendInput(context.Context, *pb.ControlRequest) (*pb.Ack, error) {
	return nil, status.Error(codes.Unimplemented, "batch profiles do not accept live input")
}
func (s *Server) RespondApproval(context.Context, *pb.ControlRequest) (*pb.Ack, error) {
	return nil, status.Error(codes.Unimplemented, "batch profiles do not support native approval responses")
}
