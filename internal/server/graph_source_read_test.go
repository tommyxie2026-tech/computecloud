package server

import (
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"testing"
)

func TestGraphSourceReadIsBoundedDurableAndIdempotent(t *testing.T) {
	h := newOfflineGraphHarness(t)
	spec := graphSpec(h)
	spec.Sources = []graph.Source{{ID: "original", Role: "user", MessageIndex: 2, Text: "原始信息🙂不能丢失"}}
	j, e := h.s.SubmitGraph(h.ctx, "read-source", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	text := spec.Sources[0].Text
	in := GraphSourceReadRequest{ID: "first", NodeID: "extract", PlanHash: j.graph.Plan.Digest, Span: graph.Span{Source: "original", Start: 0, End: len(text), SHA256: graph.Hash([]byte(text))}}
	first, e := h.s.ReadGraphSource(h.ctx, j.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if first.Text != text || first.Role != "user" || first.MessageIndex != 2 || first.SHA256 != graph.Hash([]byte(text)) {
		t.Fatal("source identity lost")
	}
	again, e := h.s.ReadGraphSource(h.ctx, j.ID, in)
	if e != nil || again.ReceiptID != first.ReceiptID {
		t.Fatal("non-idempotent read", e)
	}
	in.Span.End = 6
	if _, e = h.s.ReadGraphSource(h.ctx, j.ID, in); e == nil {
		t.Fatal("conflicting id reused")
	}
	cfg := h.s.cfg
	if e = h.s.Close(); e != nil {
		t.Fatal(e)
	}
	h.s, e = New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	in.ID = "second"
	in.ExpectedReads = 1
	if _, e = h.s.ReadGraphSource(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	in.ID = "third"
	in.ExpectedReads = 2
	if _, e = h.s.ReadGraphSource(h.ctx, j.ID, in); e == nil {
		t.Fatal("read budget reset after restart")
	}
	var contexts int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_nodes WHERE goal_id=? AND context_json IS NOT NULL`, j.graph.GoalID).Scan(&contexts)
	if contexts != 0 {
		t.Fatal("read silently activated task")
	}
}
func TestGraphSourceReadRejectsWrongPlanOrUTF8Boundary(t *testing.T) {
	h := newOfflineGraphHarness(t)
	spec := graphSpec(h)
	spec.Sources = []graph.Source{{ID: "original", Text: "中文🙂"}}
	j, e := h.s.SubmitGraph(h.ctx, "read-source", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	in := GraphSourceReadRequest{ID: "read", NodeID: "extract", PlanHash: j.graph.Plan.Digest, Span: graph.Span{Source: "original", Start: 1, End: 6, SHA256: graph.Hash([]byte("中文🙂"))}}
	if _, e = h.s.ReadGraphSource(h.ctx, j.ID, in); e == nil {
		t.Fatal("UTF-8 fragment accepted")
	}
	in.Span.Start = 0
	in.PlanHash = graph.Hash([]byte("other"))
	if _, e = h.s.ReadGraphSource(h.ctx, j.ID, in); e == nil {
		t.Fatal("wrong plan accepted")
	}
}
