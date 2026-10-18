package server

import (
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"os"
	"testing"
)

func TestCoverageNativeFailReadReplanPassAndFrozenHistory(t *testing.T) {
	h := newJobHarness(t, true, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Jobs.GraphCoverageVerifiers = map[string]graph.CoverageVerifierProfile{"orders": {Version: "1", Kind: "orders-v1", Scope: "orders", TimeoutMS: 1000}}
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate", "goals:propose")
	})
	raw, e := os.ReadFile("../../examples/iu-graph/coverage-v5/orders.json")
	if e != nil {
		t.Fatal(e)
	}
	spec, e := graph.Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	base := graphSpec(h)
	spec.Workspace = base.Workspace
	spec.ProjectID = base.ProjectID
	spec.Operators[0].Execution = base.Operators[0].Execution
	spec.Nodes[0].Instructions += " fixture-coverage-v5"
	j, e := h.s.SubmitGraph(h.ctx, "native-coverage", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	j = h.wait(t, j.ID)
	if j.State != "SUCCEEDED" {
		t.Fatalf("native failed: %+v", j)
	}
	var oldContext []byte
	if e = h.s.db.SQL.QueryRow(`SELECT context_json FROM iu_nodes WHERE goal_id=? AND generation=1 AND node_id='extract'`, j.graph.GoalID).Scan(&oldContext); e != nil {
		t.Fatal(e)
	}
	p := makeRecoveryProposal(t, h, j)
	p.ProposedSpec.Nodes[0].Instructions += " fixture-coverage-repaired"
	p.SHA256 = p.Digest()
	next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
	if e != nil || !d.Allowed {
		t.Fatal(d, e)
	}
	next = h.wait(t, next.ID)
	if next.State != "SUCCEEDED" {
		t.Fatal(next.State, next.ErrorCode)
	}
	in := coverageRequest(t, h, next)
	r, e := h.s.VerifyGraphCoverage(h.ctx, next.ID, in)
	if e != nil || r.Verdict != "PASS" {
		t.Fatal(r, e)
	}
	ev := GraphEvaluation{ID: "done", OutputHash: in.OutputHash, Verifier: spec.Evaluator.Profile, VerifierVersion: spec.Evaluator.Version, Verdict: "PASS", EvidenceHash: r.EvidenceHash, CoverageProof: &GraphCoverageProofRef{ReceiptID: in.ID, SHA256: graph.Hash(job.JSON(r))}}
	if e = h.s.EvaluateGraph(h.ctx, next.ID, ev); e != nil {
		t.Fatal(e)
	}
	var preserved []byte
	var count int
	h.s.db.SQL.QueryRow(`SELECT context_json FROM iu_nodes WHERE goal_id=? AND generation=1 AND node_id='extract'`, j.graph.GoalID).Scan(&preserved)
	if string(oldContext) != string(preserved) {
		t.Fatal("rewrote old input")
	}
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id IN (?,?)`, j.ID, next.ID).Scan(&count)
	if count != 2 {
		t.Fatal("actual attempts", count)
	}
	t.Logf("IU_COVERAGE_NATIVE goal=%s initial=%s next=%s generations=1,2 attempts=%d provider_calls=0", p.GoalID, j.ID, next.ID, count)
}
func TestRecoveryServerRestartDoesNotResetLimits(t *testing.T) {
	h, j, p := recoverySource(t)
	next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
	if e != nil || !d.Allowed {
		t.Fatal(d, e)
	}
	cfg := h.s.cfg
	if e = h.s.Close(); e != nil {
		t.Fatal(e)
	}
	h.s, e = New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	again, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
	if e != nil || !d.Existing || again.ID != next.ID {
		t.Fatal(d, e)
	}
	reads, e := h.s.GraphSourceReads(h.ctx, j.ID, "extract")
	if e != nil || reads.ReadsUsed != 1 {
		t.Fatal(reads, e)
	}
	var count int
	if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_recovery_publications WHERE goal_id=?`, p.GoalID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
