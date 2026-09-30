package worker

import (
	"context"
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	envreg "github.com/tommyxie2026-tech/computecloud/internal/environment"
	"testing"
)

func TestMetricsFailureReleasesEnvironment(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "confirmed"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			w, a, closeFn := environmentAssignment(t, "metrics-failure", unknown)
			defer closeFn()
			// Fail only metrics persistence, leaving completion and release journaling
			// available so the cleanup contract can be checked independently.
			_, err := w.db.SQL.Exec(`CREATE TRIGGER reject_metrics BEFORE INSERT ON events
    WHEN json_extract(NEW.body,'$.type')='attempt.metrics'
    BEGIN SELECT RAISE(ABORT,'metrics fixture failure'); END`)
			if err != nil {
				t.Fatal(err)
			}
			w.execute(context.Background(), a)
			var completion []byte
			var cleanup, state string
			if err = w.db.SQL.QueryRow("SELECT completion,environment_cleanup,environment_state FROM runs WHERE id=?", a.AttemptId).Scan(&completion, &cleanup, &state); err != nil {
				t.Fatal(err)
			}
			done := new(pb.CompleteRequest)
			if err = dec(completion, done); err != nil {
				t.Fatal(err)
			}
			if done.Success || done.ErrorCode != "STORAGE_UNAVAILABLE" || done.CleanupConfirmed == unknown {
				t.Fatalf("completion=%+v", done)
			}
			if unknown {
				if cleanup != string(envreg.CleanupUnknown) || state != string(envreg.StateUnknown) {
					t.Fatalf("unknown cleanup=%s state=%s", cleanup, state)
				}
			} else if cleanup != string(envreg.CleanupConfirmed) || state != string(envreg.StateReleased) {
				t.Fatalf("environment not released: %s %s", cleanup, state)
			}
		})
	}
}
