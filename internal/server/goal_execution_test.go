package server

import (
	"context"
	"errors"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestGoalSyntheticGraphExecutionAndEvaluation(t *testing.T) {
	for _, mode := range []string{"single", "report_merge_v1"} {
		t.Run(mode, func(t *testing.T) {
			h := newJobHarness(t, true)
			j, err := h.s.SubmitJob(h.ctx, "goal-"+mode, job.JSON(h.spec(mode)))
			if err != nil {
				t.Fatal(err)
			}
			replay, err := h.s.SubmitJob(h.ctx, "goal-"+mode, job.JSON(h.spec(mode)))
			if err != nil || replay.ID != j.ID {
				t.Fatal("idempotency failed")
			}
			done := h.wait(t, j.ID)
			if done.State != "SUCCEEDED" {
				t.Fatalf("job=%+v", done)
			}
			var state string
			var used, reservations, evaluations int
			if err = h.s.db.SQL.QueryRow(`SELECT state,consumed_attempts FROM goals WHERE id=?`, "goal_"+j.ID).Scan(&state, &used); err != nil {
				t.Fatal(err)
			}
			if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM goal_attempt_reservations WHERE goal_id=?", "goal_"+j.ID).Scan(&reservations); err != nil {
				t.Fatal(err)
			}
			if state != "SUCCEEDED" || used != reservations || used != len(compileJobGraph(h.spec(mode))) {
				t.Fatalf("state=%s used=%d reservations=%d", state, used, reservations)
			}
			if err = h.s.db.Tx(h.ctx, func(q store.Query) error { return evaluateBoundJob(h.ctx, q, done) }); err != nil {
				t.Fatal(err)
			}
			if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM goal_evaluations WHERE goal_id=?", "goal_"+j.ID).Scan(&evaluations); err != nil || evaluations != 1 {
				t.Fatalf("evaluation replay=%d %v", evaluations, err)
			}
			code, _ := h.request(t, "GET", "/v1/jobs/"+j.ID+"/goal", "", nil)
			if code != 200 {
				t.Fatalf("goal projection=%d", code)
			}
		})
	}
}

func TestGoalReservationRollsBackWithAttempt(t *testing.T) {
	s, ctx, _, peer := offlineJobServer(t)
	defer s.Close()
	spec := (&jobHarness{commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).spec("single")
	j, err := s.SubmitJob(ctx, "goal-rollback", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	children, err := jobChildren(ctx, s.db.SQL, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("crash before commit")
	err = s.db.Tx(ctx, func(q store.Query) error {
		a := &pb.Assignment{AttemptId: "reservation-rollback", TaskId: children[0].id, Generation: 1}
		if _, e := q.ExecContext(ctx, `INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until) VALUES(?,?,?,?,1,'token',?)`, a.AttemptId, a.TaskId, peer.hello.WorkerId, peer.hello.Epoch, store.Now()+10000); e != nil {
			return e
		}
		if e := reserveGoalAttempt(ctx, q, j, a); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var used, attempts int
	s.db.SQL.QueryRow("SELECT consumed_attempts FROM goals WHERE id=?", "goal_"+j.ID).Scan(&used)
	s.db.SQL.QueryRow("SELECT count(*) FROM attempts WHERE id='reservation-rollback'").Scan(&attempts)
	if used != 0 || attempts != 0 {
		t.Fatalf("non-atomic reservation used=%d attempts=%d", used, attempts)
	}
	if _, err = s.db.SQL.Exec("UPDATE goals SET active_graph_generation=2 WHERE id=?", "goal_"+j.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.assign(ctx, children[0].id, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var blocker string
	if err = s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", children[0].id).Scan(&blocker); err != nil || blocker != "GOAL_EXECUTION_FENCED" {
		t.Fatalf("stale graph=%s %v", blocker, err)
	}
}

func TestGoalEvaluationRejectsMissingArtifactProof(t *testing.T) {
	s, ctx, _, _ := offlineJobServer(t)
	defer s.Close()
	spec := (&jobHarness{commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).spec("single")
	j, err := s.SubmitJob(ctx, "goal-evidence", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Tx(context.Background(), func(q store.Query) error { return jobState(ctx, q, j, "SUCCEEDED", "", "") }); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.SQL.QueryRow("SELECT state FROM goals WHERE id=?", "goal_"+j.ID).Scan(&state); err != nil || state != "FAILED" {
		t.Fatalf("unproven success=%s %v", state, err)
	}
}

func TestGoalHistoricalAdoptionAndRestart(t *testing.T) {
	s, ctx, _, peer := offlineJobServer(t)
	defer func() { s.Close() }()
	spec := (&jobHarness{commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).spec("single")
	j, err := s.SubmitJob(ctx, "goal-adoption", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	children, err := jobChildren(ctx, s.db.SQL, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.assign(ctx, children[0].id, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	// Reconstruct a pre-integration Job with an actual durable Attempt.
	if err = s.db.Tx(ctx, func(q store.Query) error {
		for _, table := range []string{"goal_attempt_reservations", "goal_evaluations", "goal_plans", "goal_job_bindings"} {
			if _, e := q.ExecContext(ctx, "DELETE FROM "+table+" WHERE goal_id=?", "goal_"+j.ID); e != nil {
				return e
			}
		}
		_, e := q.ExecContext(ctx, "DELETE FROM goals WHERE id=?", "goal_"+j.ID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	cfg := s.cfg
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.reconcileGoalBindings(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var used, attempts, reservations int
	for _, check := range []struct {
		query string
		out   *int
	}{
		{"SELECT consumed_attempts FROM goals WHERE id='goal_" + j.ID + "'", &used},
		{"SELECT count(*) FROM attempts WHERE task='" + children[0].id + "'", &attempts},
		{"SELECT count(*) FROM goal_attempt_reservations WHERE goal_id='goal_" + j.ID + "'", &reservations},
	} {
		if err = s.db.SQL.QueryRow(check.query).Scan(check.out); err != nil {
			t.Fatal(err)
		}
	}
	if used != 1 || attempts != 1 || reservations != 1 {
		t.Fatalf("adoption replayed execution: used=%d attempts=%d reservations=%d", used, attempts, reservations)
	}
	// A stale evaluation cannot close a newer graph.
	if _, err = s.db.SQL.Exec("UPDATE goals SET active_graph_generation=2 WHERE id=?", "goal_"+j.ID); err != nil {
		t.Fatal(err)
	}
	j.State = "FAILED"
	if err = s.db.Tx(ctx, func(q store.Query) error { return evaluateBoundJob(ctx, q, j) }); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.SQL.QueryRow("SELECT state FROM goals WHERE id=?", "goal_"+j.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "RUNNING" {
		t.Fatalf("stale evaluator changed state: %s", state)
	}
}
