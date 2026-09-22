package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (w *Worker) flushLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if e := w.flush(ctx); e != nil && ctx.Err() == nil {
			slog.Warn("event delivery pending", "error", e)
		}
		if pause(ctx, 200*time.Millisecond) != nil {
			return
		}
	}
}
func (w *Worker) flush(ctx context.Context) error {
	rows, e := w.db.SQL.QueryContext(ctx, "SELECT assignment,ack,completion,uploaded FROM runs WHERE completed=0")
	if e != nil {
		return e
	}
	type item struct {
		assignment, completion []byte
		ack                    int64
		uploaded               bool
	}
	var list []item
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.assignment, &v.ack, &v.completion, &v.uploaded); e != nil {
			break
		}
		list = append(list, v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	for _, v := range list {
		a := new(pb.Assignment)
		if e = dec(v.assignment, a); e != nil {
			return e
		}
		rows, e := w.db.SQL.QueryContext(ctx, "SELECT body FROM events WHERE attempt=? AND seq>? ORDER BY seq LIMIT 16", a.AttemptId, v.ack)
		if e != nil {
			return e
		}
		var events []*pb.Event
		total := 0
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				break
			}
			if total+len(b) > 6<<20 && len(events) > 0 {
				break
			}
			event := new(pb.Event)
			if e = dec(b, event); e != nil {
				break
			}
			events = append(events, event)
			total += len(b)
		}
		re = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if re != nil {
			return re
		}
		if len(events) > 0 {
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			ack, err := w.client.ReportEvents(callCtx, &pb.ReportRequest{Attempt: ref(a), Events: events})
			cancel()
			if err != nil {
				return err
			}
			if ack.ThroughSeq < events[len(events)-1].WorkerSeq {
				return io.ErrUnexpectedEOF
			}
			if _, e = w.db.SQL.ExecContext(ctx, "UPDATE runs SET ack=? WHERE id=?", ack.ThroughSeq, a.AttemptId); e != nil {
				return e
			}
			v.ack = ack.ThroughSeq
			if _, e = w.db.SQL.ExecContext(ctx, "DELETE FROM events WHERE attempt=? AND seq<=?", a.AttemptId, v.ack); e != nil {
				return e
			}
		}
		if len(v.completion) == 0 {
			continue
		}
		done := new(pb.CompleteRequest)
		if e = dec(v.completion, done); e != nil {
			return e
		}
		if v.ack < done.FinalWorkerSeq {
			continue
		}
		if !v.uploaded {
			for _, id := range done.ArtifactIds {
				if e = w.upload(ctx, a, id); e != nil {
					if status.Code(e) != codes.InvalidArgument && status.Code(e) != codes.ResourceExhausted && !os.IsNotExist(e) {
						return e
					}
					// No completion has been sent until uploaded=1 is durable.
					// A permanent artifact failure must not pin a cleaned-up execution forever.
					done.Success = false
					done.ErrorCode = "ARTIFACT_ERROR"
					done.ErrorMessage = "result artifact unavailable or rejected by server limits"
					done.ArtifactIds = nil
					break
				}
			}
			if _, e = w.db.SQL.ExecContext(ctx, "UPDATE runs SET uploaded=1,completion=? WHERE id=?", enc(done), a.AttemptId); e != nil {
				return e
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ack, e := w.client.CompleteAttempt(callCtx, done)
		cancel()
		if e != nil {
			return e
		}
		if ack.State == "COMMITTED" || ack.State == "RECONCILING" {
			if _, e = w.db.SQL.ExecContext(ctx, "UPDATE runs SET completed=1 WHERE id=?", a.AttemptId); e != nil {
				return e
			}
		}
	}
	return nil
}
func (w *Worker) upload(ctx context.Context, a *pb.Assignment, id string) error {
	dir := filepath.Join(w.cfg.DataDir, "artifacts")
	b, e := os.ReadFile(filepath.Join(dir, id+".json"))
	if e != nil {
		return e
	}
	m := new(pb.Artifact)
	if e = dec(b, m); e != nil {
		return e
	}
	f, e := os.Open(filepath.Join(dir, id))
	if e != nil {
		return e
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stream, e := w.client.UploadArtifact(ctx)
	if e != nil {
		return e
	}
	if e = stream.Send(&pb.ArtifactChunk{Metadata: m, Attempt: ref(a)}); e != nil {
		return e
	}
	buf := make([]byte, 64<<10)
	for {
		n, e := f.Read(buf)
		if n > 0 {
			if e = stream.Send(&pb.ArtifactChunk{Data: buf[:n]}); e != nil {
				if _, remoteErr := stream.CloseAndRecv(); remoteErr != nil {
					return remoteErr
				}
				return e
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	_, e = stream.CloseAndRecv()
	return e
}
