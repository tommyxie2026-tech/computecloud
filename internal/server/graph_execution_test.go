package server

import (
	"encoding/json"
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

func TestGraphInputBindingUsesConsumerPortAndRetainsProducerIdentity(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	spec := graphSpec(h)
	op := spec.Operators[0]
	op.ID = "consume"
	op.Inputs = []graph.Port{{Name: "amount", Schema: "report-v1"}}
	spec.Operators = append(spec.Operators, op)
	spec.Nodes = append(spec.Nodes, graph.Node{ID: "consume", Operator: "consume"})
	spec.Edges = []graph.Edge{{FromNode: "extract", FromPort: "result", ToNode: "consume", ToPort: "amount", Type: "memory"}}
	spec.Outputs = []graph.Output{{Node: "consume", Port: "result"}}
	j, e := h.s.SubmitGraph(h.ctx, "port-binding", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e = h.s.db.SQL.QueryRow("SELECT context_json FROM iu_nodes WHERE goal_id=? AND node_id='consume'", j.graph.GoalID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var manifest graph.ContextManifest
	if e = json.Unmarshal(raw, &manifest); e != nil {
		t.Fatal(e)
	}
	var context struct {
		Inputs []struct {
			Node       string
			Port       string
			SourcePort string `json:"source_port"`
		}
	}
	if e = json.Unmarshal([]byte(manifest.Text), &context); e != nil {
		t.Fatal(e)
	}
	if len(context.Inputs) != 1 || context.Inputs[0].Node != "extract" || context.Inputs[0].Port != "amount" || context.Inputs[0].SourcePort != "result" {
		t.Fatalf("wrong input binding: %s", manifest.Text)
	}
}
