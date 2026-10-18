package server

import (
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"os"
	"path/filepath"
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
func coverageJob(t *testing.T, total int) (*jobHarness, *Job) {
	t.Helper()
	h := newJobHarness(t, false, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Jobs.GraphCoverageVerifiers = map[string]graph.CoverageVerifierProfile{"orders": {Version: "1", Kind: "orders-v1", Scope: "orders", TimeoutMS: 1000}}
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate", "goals:propose")
	})
	raw, e := os.ReadFile("../../examples/iu-graph/coverage-v5/orders.json")
	if e != nil {
		t.Fatal(e)
	}
	s, e := graph.Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	base := graphSpec(h)
	s.Workspace = base.Workspace
	s.ProjectID = base.ProjectID
	s.Operators[0].Execution = base.Operators[0].Execution
	j, e := h.s.SubmitGraph(h.ctx, "coverage", job.JSON(s))
	if e != nil {
		t.Fatal(e)
	}
	finishCoverageJob(t, h, j, total)
	return h, j
}
func finishCoverageJob(t *testing.T, h *jobHarness, j *Job, total int) {
	var out graph.ExecutionOutput
	if e := json.Unmarshal(graphCandidateOutput(t, h, j), &out); e != nil {
		t.Fatal(e)
	}
	out.Values["result"] = json.RawMessage(fmt.Sprintf(`{"orders":[{"order_id":"a","customer_id":"c","paid_cents":6000,"is_special":false},{"order_id":"b","customer_id":"c","paid_cents":6000,"is_special":false}],"total_cents":%d,"currency":"CNY","unit":"cent"}`, total))
	seedGraphCandidate(t, h, j, job.JSON(out))
	if e := h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e := h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
}
func coverageRequest(t *testing.T, h *jobHarness, j *Job) GraphCoverageRequest {
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	return GraphCoverageRequest{ID: "proof", OutputHash: st.OutputHash, ExpectedValidityRevision: st.ValidityRevision, Profile: "orders", ProfileVersion: "1"}
}
func TestCoverageReceiptRequiresIndependentAuthorityAndPinsEvidence(t *testing.T) {
	h, j := coverageJob(t, 11000)
	in := coverageRequest(t, h, j)
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.Identity.Scopes = []string{"jobs:read"}
	if _, e := h.s.VerifyGraphCoverage(rpcutil.WithPrincipal(h.ctx, p), j.ID, in); e == nil {
		t.Fatal("unauthorized")
	}
	r, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in)
	if e != nil || r.Verdict != "PASS" {
		t.Fatal(r, e)
	}
	again, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in)
	if e != nil || graph.Hash(job.JSON(r)) != graph.Hash(job.JSON(again)) {
		t.Fatal("replay", e)
	}
	var n int
	if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM artifact_refs WHERE ref_id LIKE 'coverage/%'`).Scan(&n); e != nil || n == 0 {
		t.Fatal(n, e)
	}
	in.OutputHash = graph.Hash([]byte("different"))
	if _, e = h.s.VerifyGraphCoverage(h.ctx, j.ID, in); e == nil {
		t.Fatal("forged output")
	}
}
func TestCoveragePassRequiresCurrentCompleteReceipt(t *testing.T) {
	h, j := coverageJob(t, 11000)
	in := coverageRequest(t, h, j)
	ev := GraphEvaluation{ID: "done", OutputHash: in.OutputHash, Verifier: j.graph.Plan.Spec.Evaluator.Profile, VerifierVersion: j.graph.Plan.Spec.Evaluator.Version, Verdict: "PASS", EvidenceHash: in.OutputHash}
	if e := h.s.EvaluateGraph(h.ctx, j.ID, ev); e == nil {
		t.Fatal("unproved pass")
	}
	r, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	ev.CoverageProof = &GraphCoverageProofRef{ReceiptID: in.ID, SHA256: graph.Hash(job.JSON(r))}
	old := h.s.cfg.Jobs.GraphCoverageVerifiers["orders"]
	changed := old
	changed.TimeoutMS = 999
	h.s.cfg.Jobs.GraphCoverageVerifiers["orders"] = changed
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e == nil {
		t.Fatal("drift pass")
	}
	h.s.cfg.Jobs.GraphCoverageVerifiers["orders"] = old
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
}
func TestCoverageReceiptCannotLaunderUnknownOrCorrectTotal(t *testing.T) {
	h, j := coverageJob(t, 12000)
	in := coverageRequest(t, h, j)
	r, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in)
	if e != nil || r.Verdict != "FAIL" {
		t.Fatal(r, e)
	}
	ev := GraphEvaluation{ID: "lie", OutputHash: in.OutputHash, Verifier: j.graph.Plan.Spec.Evaluator.Profile, VerifierVersion: j.graph.Plan.Spec.Evaluator.Version, Verdict: "PASS", EvidenceHash: in.OutputHash, CoverageProof: &GraphCoverageProofRef{ReceiptID: in.ID, SHA256: graph.Hash(job.JSON(r))}}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e == nil {
		t.Fatal("wrong total passed")
	}
}
func TestCoverageReceiptQuotaAndFailedWritesUnpin(t *testing.T) {
	h, j := coverageJob(t, 11000)
	in := coverageRequest(t, h, j)
	for i := 0; i < 128; i++ {
		in.ID = fmt.Sprintf("quota-%d", i)
		if _, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in); e != nil {
			t.Fatal(i, e)
		}
	}
	in.ID = "overflow"
	if _, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in); e == nil {
		t.Fatal("receipt quota bypass")
	}
	var refs int
	if e := h.s.db.SQL.QueryRow(`SELECT count(*) FROM artifact_refs WHERE ref_id LIKE 'coverage/%/overflow'`).Scan(&refs); e != nil || refs != 0 {
		t.Fatal("failed receipt left pin", refs, e)
	}
}
func TestCoveragePassRejectsLostArtifactEvidence(t *testing.T) {
	h, j := coverageJob(t, 11000)
	in := coverageRequest(t, h, j)
	r, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	var path string
	if e = h.s.db.SQL.QueryRow(`SELECT path FROM artifacts WHERE id=?`, r.Artifacts[0]).Scan(&path); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(h.s.cfg.DataDir, "artifacts", path)); e != nil {
		t.Fatal(e)
	}
	ev := GraphEvaluation{ID: "lost", OutputHash: in.OutputHash, Verifier: j.graph.Plan.Spec.Evaluator.Profile, VerifierVersion: j.graph.Plan.Spec.Evaluator.Version, Verdict: "PASS", EvidenceHash: r.EvidenceHash, CoverageProof: &GraphCoverageProofRef{ReceiptID: in.ID, SHA256: graph.Hash(job.JSON(r))}}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e == nil {
		t.Fatal("lost evidence passed")
	}
}
