package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func graphSpec(h *jobHarness) graph.Spec {
	legacy := h.spec("single")
	ex := *legacy.Execution
	return graph.Spec{Schemas: map[string]json.RawMessage{"report-v1": json.RawMessage(`{}`)}, Evaluator: graph.Evaluator{Profile: "external", Version: "1"}, Version: graph.Version, ProjectID: "project", Workspace: legacy.Workspace, Sources: []graph.Source{}, Operators: []graph.Operator{{ID: "extract", Version: "1", Kind: "agent", Execution: &ex, Outputs: []graph.Port{{Name: "result", Schema: "report-v1"}}}}, Nodes: []graph.Node{{ID: "extract", Operator: "extract", Instructions: "read source"}}, Edges: []graph.Edge{}, Outputs: []graph.Output{{Node: "extract", Port: "result"}}, Limits: graph.Limits{Parallelism: 2, TimeoutSeconds: 60, MaxAttemptsPerTask: 1}}
}

func TestGraphSubmitIsDefaultOff(t *testing.T) {
	h := newJobHarness(t, false)
	if _, e := h.s.SubmitGraph(h.ctx, "graph", job.JSON(graphSpec(h))); e == nil || !strings.Contains(e.Error(), "GRAPHS_DISABLED") {
		t.Fatalf("%v", e)
	}
}

func TestGraphSubmitNamespacesAndIdempotency(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	spec := graphSpec(h)
	a, e := h.s.SubmitGraph(h.ctx, "a", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	b, e := h.s.SubmitGraph(h.ctx, "b", job.JSON(spec))
	if e != nil || b.ID == a.ID {
		t.Fatalf("second graph: %v", e)
	}
	again, e := h.s.SubmitGraph(h.ctx, "a", job.JSON(spec))
	if e != nil || again.ID != a.ID || !again.Existing {
		t.Fatalf("replay: %v", e)
	}
	spec.Nodes[0].Instructions = "changed"
	if _, e = h.s.SubmitGraph(h.ctx, "a", job.JSON(spec)); e == nil {
		t.Fatal("conflicting key accepted")
	}
	status, e := h.s.GraphStatus(h.ctx, a.ID)
	if e != nil || status.GoalID == "" || status.Generation != 1 || len(status.Nodes) != 1 {
		t.Fatalf("status %+v %v", status, e)
	}
	spec.ProjectID = "other"
	if _, e = h.s.SubmitGraph(h.ctx, "bad", job.JSON(spec)); e == nil {
		t.Fatal("unauthorized project")
	}
	spec.ProjectID = "project"
	spec.Operators[0].Execution.CredentialRef = "other"
	if _, e = h.s.SubmitGraph(h.ctx, "credential", job.JSON(spec)); e == nil {
		t.Fatal("unauthorized credential")
	}
}
