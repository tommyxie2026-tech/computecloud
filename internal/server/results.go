package server

import (
	"context"
	"encoding/json"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) ReportEvents(ctx context.Context, r *pb.ReportRequest) (*pb.Ack, error) {
	p, e := rpcutil.Worker(ctx)
	if e != nil {
		return nil, e
	}
	if len(r.Events) > 64 {
		return nil, status.Error(codes.ResourceExhausted, "event batch too large")
	}
	var through int64
	e = s.db.Tx(ctx, func(q store.Query) error {
		a, e := s.checkAttempt(ctx, q, p.Identity.WorkerID, r.Attempt)
		if e != nil {
			return e
		}
		through = a.seq
		for _, ev := range r.Events {
			if ev.TaskId != a.task || ev.AttemptId != r.Attempt.AttemptId || ev.Generation != a.generation || ev.EventId == "" || ev.WorkerSeq < 1 || ev.Seq != 0 || ev.RecordedAtMs != 0 || len(ev.PayloadJson) > 4<<20 || !json.Valid(ev.PayloadJson) {
				return status.Error(codes.InvalidArgument, "invalid event identity or payload")
			}
			if ev.Type == "task.completed" || ev.Type == "task.state_changed" {
				return status.Error(codes.PermissionDenied, "task state events are server-owned")
			}
			if ev.WorkerSeq <= through {
				var hash string
				if e = q.QueryRowContext(ctx, "SELECT hash FROM event_dedup WHERE attempt=? AND worker_seq=?", r.Attempt.AttemptId, ev.WorkerSeq).Scan(&hash); e != nil {
					return e
				}
				if hash != store.Hash(encode(ev)) {
					return status.Error(codes.AlreadyExists, "event hash conflict")
				}
				continue
			}
			if a.released {
				return status.Error(codes.FailedPrecondition, "attempt already completed")
			}
			if ev.WorkerSeq != through+1 {
				return status.Error(codes.FailedPrecondition, "event gap")
			}
			wseq := ev.WorkerSeq
			if e = appendEvent(ctx, q, ev, &wseq); e != nil {
				return e
			}
			through = wseq
			if ev.Type == "attempt.started" {
				t, _, e := readTask(ctx, q, a.task)
				if e != nil {
					return e
				}
				allowed, e := s.jobAllowsExecution(ctx, q, a.task)
				if e != nil {
					return e
				}
				if t.State == "STARTING" && a.until > store.Now() && allowed {
					if e = setState(ctx, q, a.task, "RUNNING", "", ""); e != nil {
						return e
					}
				}
			}
		}
		_, e = q.ExecContext(ctx, "UPDATE attempts SET worker_seq=? WHERE id=?", through, r.Attempt.AttemptId)
		return e
	})
	if e != nil {
		return nil, dbErr(e)
	}
	return &pb.Ack{ThroughSeq: through, State: "COMMITTED"}, nil
}
func (s *Server) CompleteAttempt(ctx context.Context, r *pb.CompleteRequest) (*pb.Ack, error) {
	p, e := rpcutil.Worker(ctx)
	if e != nil {
		return nil, e
	}
	if len(r.Result) > 1<<20 || len(r.ErrorMessage) > 8192 || r.FinalWorkerSeq < 0 {
		return nil, status.Error(codes.ResourceExhausted, "completion too large")
	}
	hash := store.Hash(encode(r))
	e = s.db.Tx(ctx, func(q store.Query) error {
		a, e := s.readAttemptIdentity(ctx, q, p.Identity.WorkerID, r.Attempt)
		if e != nil {
			return e
		}
		epochChanged := a.currentEpoch != "" && a.epoch != a.currentEpoch
		if a.currentEpoch == "" {
			return status.Error(codes.FailedPrecondition, "STALE_ATTEMPT")
		}
		// A restarted Worker gets a new epoch. It may prove that the old
		// process was cleaned up, but it may never turn old-epoch work into
		// success or publish artifacts.
		if epochChanged && (!r.CleanupConfirmed || r.Success || r.ErrorCode != "WORKER_RESTARTED" || len(r.ArtifactIds) != 0) {
			return status.Error(codes.FailedPrecondition, "STALE_ATTEMPT")
		}
		if a.finalHash != "" {
			if a.finalHash != hash {
				return status.Error(codes.AlreadyExists, "completion hash conflict")
			}
			return nil
		}
		if !epochChanged && a.seq != r.FinalWorkerSeq {
			return status.Error(codes.FailedPrecondition, "final event watermark not committed")
		}
		t, _, e := readTask(ctx, q, a.task)
		if e != nil {
			return e
		}
		if !r.CleanupConfirmed {
			if t.State != "CANCELING" {
				return setState(ctx, q, t.TaskId, "RECONCILING", "CLEANUP_UNCONFIRMED", "worker could not prove cleanup")
			}
			return nil
		}
		for _, id := range r.ArtifactIds {
			var n int
			if e = q.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE id=? AND attempt=? AND generation=? AND state='STAGED'", id, r.Attempt.AttemptId, a.generation).Scan(&n); e != nil {
				return e
			}
			if n != 1 {
				return status.Error(codes.FailedPrecondition, "artifact not registered for current generation")
			}
		}
		state, code, msg := "FAILED", r.ErrorCode, r.ErrorMessage
		allowed, e := s.jobAllowsExecution(ctx, q, a.task)
		if e != nil {
			return e
		}
		if t.State == "CANCELING" {
			if t.ErrorCode == "DEADLINE_EXCEEDED" {
				code = t.ErrorCode
				msg = t.ErrorMessage
			} else {
				state = "CANCELED"
				code = ""
				msg = ""
			}
		} else if epochChanged {
			code = "WORKER_RESTARTED"
			msg = "execution interrupted by worker restart"
		} else if t.State == "RECONCILING" || a.until <= store.Now() {
			code = "WORKER_LOST"
			msg = "lease lost; execution stopped"
		} else if !allowed {
			code = "JOB_STOPPING"
			msg = "job no longer accepts success"
		} else if r.Success {
			if len(r.ArtifactIds) == 0 {
				return status.Error(codes.FailedPrecondition, "successful task needs verification artifact")
			}
			var managed int
			if e = q.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE id=? AND job_id IS NOT NULL", a.task).Scan(&managed); e != nil {
				return e
			}
			if managed != 0 {
				var bundles int
				if len(r.ArtifactIds) == 1 {
					if e = q.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE id=? AND kind='result-bundle'", r.ArtifactIds[0]).Scan(&bundles); e != nil {
						return e
					}
				}
				if bundles != 1 {
					return status.Error(codes.FailedPrecondition, "Job success requires exactly one result-bundle")
				}
			}
			state = "SUCCEEDED"
			code = ""
			msg = ""
			if e = setState(ctx, q, t.TaskId, "VERIFYING", "", ""); e != nil {
				return e
			}
		}
		if state == "SUCCEEDED" {
			for _, id := range r.ArtifactIds {
				if e = acceptArtifact(ctx, q, id, r.Attempt.AttemptId, t.TaskId, a.generation); e != nil {
					return status.Error(codes.FailedPrecondition, e.Error())
				}
			}
			if e = orphanStagedArtifacts(ctx, q, r.Attempt.AttemptId, a.generation); e != nil {
				return e
			}
		} else {
			if e = orphanStagedArtifacts(ctx, q, r.Attempt.AttemptId, a.generation); e != nil {
				return e
			}
		}
		if _, e = q.ExecContext(ctx, "UPDATE tasks SET result=?,native_session=? WHERE id=?", r.Result, r.NativeSessionId, t.TaskId); e != nil {
			return e
		}
		if e = appendEvent(ctx, q, &pb.Event{TaskId: t.TaskId, AttemptId: r.Attempt.AttemptId, Generation: a.generation, Type: "attempt.completed", PayloadJson: config.JSON(map[string]any{"success": r.Success, "cleanup_confirmed": true, "artifact_ids": r.ArtifactIds})}, nil); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, "UPDATE attempts SET released=1,final_hash=? WHERE id=?", hash, r.Attempt.AttemptId); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, "UPDATE commands SET acked=1 WHERE attempt=?", r.Attempt.AttemptId); e != nil {
			return e
		}
		if state == "FAILED" {
			plan, pe := s.planRetry(ctx, q, t.TaskId, code, a.generation)
			if pe != nil {
				return pe
			}
			if plan.retry {
				return s.scheduleRetry(ctx, q, t, r.Attempt.AttemptId, a.generation, code, msg, plan)
			}
			if pe = appendRetryExhausted(ctx, q, t, r.Attempt.AttemptId, a.generation, code, plan); pe != nil {
				return pe
			}
		}
		return setState(ctx, q, t.TaskId, state, code, msg)
	})
	s.wake()
	if e != nil {
		return nil, dbErr(e)
	}
	state := "COMMITTED"
	if !r.CleanupConfirmed {
		state = "RECONCILING"
	}
	return &pb.Ack{State: state}, nil
}
