package server

import (
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func TestGraphActivationIsAtomicAndNamespaceScoped(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	spec := graphSpec(h)
	spec.Nodes = append(spec.Nodes, graph.Node{ID: "next", Operator: "extract", Instructions: "next"})
	spec.Edges = append(spec.Edges, graph.Edge{FromNode: "extract", ToNode: "next", Type: "control"})
	spec.Outputs = []graph.Output{{Node: "next", Port: "result"}}
	j, e := h.s.SubmitGraph(h.ctx, "graph", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, j.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("tasks=%d %v", count, e)
	}
	status, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if status.Nodes[0].TaskID == nil || status.Nodes[1].TaskID != nil {
		t.Fatalf("unexpected activation %+v", status.Nodes)
	}
	j2, e := h.s.SubmitGraph(h.ctx, "second", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j2.ID); e != nil {
		t.Fatal(e)
	}
	if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id IN (?,?)`, j.ID, j2.ID).Scan(&count); e != nil || count != 2 {
		t.Fatalf("namespace tasks=%d %v", count, e)
	}
}

func TestGraphTerminalNodesReflectFailureAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancel"}[cancel], func(t *testing.T) {
			h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
			j, e := h.s.SubmitGraph(h.ctx, "terminal", job.JSON(graphSpec(h)))
			if e != nil {
				t.Fatal(e)
			}
			if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
				t.Fatal(e)
			}
			want := "FAILED"
			if cancel {
				want = "CANCELED"
				if _, e = h.s.CancelJob(h.ctx, j.ID, CancelJobRequest{ControlID: "stop"}); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e = h.s.db.SQL.Exec(`UPDATE tasks SET state='FAILED',error_code='RUNTIME_FAILED' WHERE job_id=?`, j.ID); e != nil {
					t.Fatal(e)
				}
			}
			if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
				t.Fatal(e)
			}
			st, e := h.s.GraphStatus(h.ctx, j.ID)
			if e != nil || st.Nodes[0].State != want {
				t.Fatalf("node terminal %+v %v", st, e)
			}
		})
	}
}

func TestGraphQueuePressureDefersActivationWithoutBlockingController(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	cfg := h.s.cfg
	cfg.DataDir = t.TempDir()
	cfg.Jobs.MaxQueuedTasks = 1
	cfg.Jobs.MaxQueuedTasksPerProject = 1
	s, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	a, e := s.SubmitGraph(h.ctx, "queue-a", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.SubmitGraph(h.ctx, "queue-b", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.advanceGraph(h.ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.advanceGraph(h.ctx, b.ID); e != nil {
		t.Fatalf("backpressure blocked controller: %v", e)
	}
	var n int
	if e = s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE state='QUEUED'").Scan(&n); e != nil || n != 1 {
		t.Fatalf("queued=%d %v", n, e)
	}
	// Model the scheduler removing the first task from its queue.
	if _, e = s.db.SQL.Exec("UPDATE tasks SET state='CANCELED' WHERE job_id=?", a.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.advanceGraph(h.ctx, b.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=?", b.ID).Scan(&n); e != nil || n != 1 {
		t.Fatalf("second graph never resumed: %d %v", n, e)
	}
}
