package server

import (
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"testing"
)

func TestCoverageSubmitCreatesNoExecutionOnGap(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	s := graphSpec(h)
	s.Version = "iu-graph-v5"
	if _, e := h.s.SubmitGraph(h.ctx, "gap", job.JSON(s)); e == nil {
		t.Fatal("missing catalog accepted")
	}
	for _, table := range []string{"jobs", "goals", "tasks", "attempts"} {
		var n int
		if e := h.s.db.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("%s=%d: %v", table, n, e)
		}
	}
}
