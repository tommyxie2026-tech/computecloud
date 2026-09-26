package server

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ExtendDeadlineRequest struct {
	OperationID   string `json:"operation_id"`
	NewDeadlineMS int64  `json:"new_deadline_ms,string"`
}

func (s *Server) ExtendJobDeadline(ctx context.Context, id string, in ExtendDeadlineRequest) (*Job, error) {
	if !job.ValidKey(in.OperationID) || in.NewDeadlineMS <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid deadline extension")
	}
	if _, err := s.jobAuthorized(ctx, id, "jobs:extend"); err != nil {
		return nil, err
	}
	replayed := false
	err := s.db.Tx(ctx, func(q store.Query) error {
		digest := job.Hash(job.JSON(in))
		var old string
		err := q.QueryRowContext(ctx, "SELECT operation_hash FROM job_events WHERE job_id=? AND operation_key=?", id, in.OperationID).Scan(&old)
		if err == nil {
			if old != digest {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			replayed = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		j, err := readJob(ctx, q, id)
		if err != nil {
			return err
		}
		if terminal(j.State) || j.StopReason != "" || j.State == "STOPPING" || j.State == "RECONCILING" {
			return status.Error(codes.FailedPrecondition, "JOB_NOT_EXTENDABLE")
		}
		now := store.Now()
		if in.NewDeadlineMS <= now || in.NewDeadlineMS <= j.Deadline {
			return status.Error(codes.InvalidArgument, "deadline must increase and remain in the future")
		}
		if in.NewDeadlineMS-j.Deadline > s.cfg.Jobs.MaxDeadlineExtendSeconds*1000 {
			return status.Error(codes.InvalidArgument, "deadline extension exceeds per-operation limit")
		}
		maxDeadline := j.Created + s.cfg.Jobs.MaxTotalRuntimeSeconds*1000
		if in.NewDeadlineMS > maxDeadline {
			return status.Error(codes.InvalidArgument, "deadline exceeds total runtime limit")
		}
		res, err := q.ExecContext(ctx, "UPDATE jobs SET deadline=?,version=version+1,updated=? WHERE id=? AND version=?", in.NewDeadlineMS, now, id, j.Version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return status.Error(codes.Aborted, "job version conflict")
		}
		if _, err = q.ExecContext(ctx, "UPDATE tasks SET deadline=? WHERE job_id=? AND state NOT IN ('SUCCEEDED','FAILED','CANCELED') AND deadline<?", in.NewDeadlineMS, id, in.NewDeadlineMS); err != nil {
			return err
		}
		return appendJobEvent(ctx, q, id, "job.deadline_extended", map[string]any{
			"from_deadline_ms": j.Deadline,
			"to_deadline_ms":   in.NewDeadlineMS,
		}, in.OperationID, digest)
	})
	if err != nil {
		return nil, dbErr(err)
	}
	s.wake()
	j, err := readJob(ctx, s.db.SQL, id)
	if err == nil {
		j.Existing = replayed
	}
	return j, dbErr(err)
}

func (s *Server) compactTaskEvents(ctx context.Context) error {
	maxEvents := s.cfg.Jobs.MaxTaskEvents
	if maxEvents <= 0 {
		return nil
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT id,seq,event_floor_seq
		FROM tasks
		WHERE seq-event_floor_seq>?
		ORDER BY updated,id
		LIMIT 32`, maxEvents)
	if err != nil {
		return err
	}
	type candidate struct {
		id         string
		seq, floor int64
	}
	var list []candidate
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.id, &v.seq, &v.floor); err != nil {
			break
		}
		list = append(list, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}

	for _, v := range list {
		target := v.seq - int64(maxEvents)
		if target <= v.floor {
			continue
		}
		if err = s.db.Tx(ctx, func(q store.Query) error {
			var currentSeq, currentFloor int64
			if e := q.QueryRowContext(ctx, "SELECT seq,event_floor_seq FROM tasks WHERE id=?", v.id).Scan(&currentSeq, &currentFloor); e != nil {
				return e
			}
			target = currentSeq - int64(maxEvents)
			if target <= currentFloor {
				return nil
			}
			if _, e := q.ExecContext(ctx, "DELETE FROM events WHERE task=? AND seq<=?", v.id, target); e != nil {
				return e
			}
			_, e := q.ExecContext(ctx, "UPDATE tasks SET event_floor_seq=? WHERE id=? AND event_floor_seq<?", target, v.id, target)
			return e
		}); err != nil {
			return err
		}
	}
	return nil
}
