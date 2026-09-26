package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func (w *Worker) accept(ctx context.Context, c *pb.Command) (bool, error) {
	if c.Assignment == nil || c.Assignment.AttemptId == "" || c.Assignment.TaskId == "" || c.Assignment.Generation < 1 || c.Assignment.LeaseToken == "" || (c.Kind != "start" && c.Kind != "stop") {
		return false, errors.New("invalid command")
	}
	a := c.Assignment
	launch := false
	e := w.db.Tx(ctx, func(q store.Query) error {
		hash := store.Hash(enc(c))
		var old string
		e := q.QueryRowContext(ctx, "SELECT hash FROM commands WHERE id=?", c.CommandId).Scan(&old)
		if e == nil {
			if old != hash {
				return errors.New("command hash conflict")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var existing []byte
		var state string
		var stop bool
		e = q.QueryRowContext(ctx, "SELECT assignment,state,stop FROM runs WHERE id=?", a.AttemptId).Scan(&existing, &state, &stop)
		if e == nil {
			original := new(pb.Assignment)
			if e = dec(existing, original); e != nil {
				return e
			}
			if original.TaskId != a.TaskId || original.Generation != a.Generation || original.LeaseToken != a.LeaseToken {
				return errors.New("attempt identity conflict")
			}
			if c.Kind == "start" && !stop && store.Hash(existing) != store.Hash(enc(a)) {
				return errors.New("start spec conflict")
			}
		} else if errors.Is(e, sql.ErrNoRows) {
			state = "ACCEPTED"
			if _, e = q.ExecContext(ctx, "INSERT INTO runs(id,assignment,state,stop) VALUES(?,?,?,?)", a.AttemptId, enc(a), state, c.Kind == "stop"); e != nil {
				return e
			}
			launch = c.Kind == "start"
		} else {
			return e
		}
		if c.Kind == "stop" {
			if _, e = q.ExecContext(ctx, "UPDATE runs SET stop=1 WHERE id=?", a.AttemptId); e != nil {
				return e
			}
			if state == "ACCEPTED" {
				completion := &pb.CompleteRequest{Attempt: ref(a), CleanupConfirmed: true, ErrorCode: "CANCELED", ErrorMessage: "stopped before launch"}
				if _, e = q.ExecContext(ctx, "UPDATE runs SET state='DONE',completion=? WHERE id=? AND state='ACCEPTED'", enc(completion), a.AttemptId); e != nil {
					return e
				}
			}
		}
		_, e = q.ExecContext(ctx, "INSERT INTO commands VALUES(?,?)", c.CommandId, hash)
		return e
	})
	return launch, e
}
func (w *Worker) emit(ctx context.Context, a *pb.Assignment, kind string, payload []byte) error {
	return w.db.Tx(ctx, func(q store.Query) error {
		var seq int64
		var bytes int64
		if e := q.QueryRowContext(ctx, "SELECT coalesce(sum(length(body)),0) FROM events WHERE attempt=?", a.AttemptId).Scan(&bytes); e != nil {
			return e
		}
		if bytes+int64(len(payload)) > 256<<20 {
			return errors.New("local event spool limit reached")
		}
		if e := q.QueryRowContext(ctx, "UPDATE runs SET seq=seq+1 WHERE id=? RETURNING seq", a.AttemptId).Scan(&seq); e != nil {
			return e
		}
		ev := &pb.Event{TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: a.Generation, EventId: store.ID(), WorkerSeq: seq, Type: kind, PayloadJson: append([]byte(nil), payload...)}
		_, e := q.ExecContext(ctx, "INSERT INTO events VALUES(?,?,?)", a.AttemptId, seq, enc(ev))
		return e
	})
}
func (w *Worker) completion(ctx context.Context, a *pb.Assignment, c *pb.CompleteRequest) error {
	if e := w.finalizeWorkspace(ctx, a, c.CleanupConfirmed); e != nil {
		if c.Success {
			c.Success = false
			c.ErrorCode = "WORKSPACE_ERROR"
			c.ErrorMessage = "workspace lifecycle persistence failed"
		} else if c.ErrorMessage == "" {
			c.ErrorMessage = "workspace lifecycle persistence failed"
		}
	}
	return w.db.Tx(ctx, func(q store.Query) error {
		if e := q.QueryRowContext(ctx, "SELECT seq FROM runs WHERE id=?", a.AttemptId).Scan(&c.FinalWorkerSeq); e != nil {
			return e
		}
		c.Attempt = ref(a)
		_, e := q.ExecContext(ctx, "UPDATE runs SET state='DONE',completion=? WHERE id=? AND completion IS NULL", enc(c), a.AttemptId)
		return e
	})
}
func (w *Worker) failUnstarted(ctx context.Context, a *pb.Assignment, code, msg string) error {
	return w.completion(ctx, a, &pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: code, ErrorMessage: msg})
}
func (w *Worker) recover(ctx context.Context) error {
	rows, e := w.db.SQL.QueryContext(ctx, "SELECT assignment,state,pid,start_id,completion FROM runs WHERE completed=0")
	if e != nil {
		return e
	}
	type record struct {
		b, completion []byte
		state, id     string
		pid           int
	}
	var all []record
	for rows.Next() {
		var r record
		if e = rows.Scan(&r.b, &r.state, &r.pid, &r.id, &r.completion); e != nil {
			break
		}
		all = append(all, r)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	for _, r := range all {
		if len(r.completion) > 0 {
			continue
		}
		a := new(pb.Assignment)
		if e = dec(r.b, a); e != nil {
			return e
		}
		clean := r.state == "ACCEPTED"
		if r.pid > 1 {
			clean = process.Stop(r.pid, r.id, time.Duration(w.cfg.StopGraceMS)*time.Millisecond)
		} else if r.state == "STARTING" && w.workspacePreSpawnSafe(ctx, a.AttemptId) {
			// PREPARING/READY is persisted before IN_USE, and IN_USE is
			// persisted before any Runtime/Verifier spawn. This closes the
			// historical STARTING+pid=0 ambiguity when the workspace itself
			// proves that no execution process could have started.
			clean = true
		}
		c := &pb.CompleteRequest{CleanupConfirmed: clean, ErrorCode: "WORKER_RESTARTED", ErrorMessage: "execution interrupted by worker restart"}
		if !clean {
			c.ErrorCode = "CLEANUP_UNCONFIRMED"
			c.ErrorMessage = "startup window requires manual inspection; no automatic restart"
		}
		// A new Worker epoch may prove cleanup, but it must never replay
		// execution events from the old epoch as live state. Skip any locally
		// unacknowledged old-epoch events and let the Server record only the
		// recovery completion.
		if _, e = w.db.SQL.ExecContext(ctx, "UPDATE runs SET ack=seq WHERE id=?", a.AttemptId); e != nil {
			return e
		}
		if e = w.completion(ctx, a, c); e != nil {
			return fmt.Errorf("recover attempt: %w", e)
		}
	}
	return nil
}
