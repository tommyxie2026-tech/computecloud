package server

import (
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"strings"
	"testing"
)

func importSource(t *testing.T) (*jobHarness, *Job, graph.Spec, GraphEvaluation) {
	t.Helper()
	h := newOfflineGraphHarness(t)
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.Identity.Scopes = append(p.Identity.Scopes, "goals:import")
	h.ctx = rpcutil.WithPrincipal(h.ctx, p)
	spec := graphSpec(h)
	spec.Limits.MaxReplans = 1
	j, e := h.s.SubmitGraph(h.ctx, "source", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	var out graph.ExecutionOutput
	if e = json.Unmarshal(graphCandidateOutput(t, h, j), &out); e != nil {
		t.Fatal(e)
	}
	out.Delta.Records = []graph.MemoryRecord{{ID: "raw", Kind: "observation", Payload: json.RawMessage(`12000`), Validity: "VALID", Validation: "unchecked"}}
	seedGraphCandidate(t, h, j, job.JSON(out))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	ev := GraphEvaluation{ID: "business-fail", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: st.OutputHash}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
	spec.Version = graph.ImportVersion
	spec.Imports = []graph.ImportBinding{{GrantID: "grant_a", NodeIDs: []string{"extract"}}}
	return h, j, spec, ev
}
func makeImportGrant(t *testing.T, h *jobHarness, j *Job, spec graph.Spec) GraphImportRequest {
	t.Helper()
	view, e := h.s.GraphMemory(h.ctx, j.ID, "extract")
	if e != nil {
		t.Fatal(e)
	}
	plan, e := graph.Compile(spec)
	if e != nil {
		t.Fatal(e)
	}
	return GraphImportRequest{ID: "grant_a", NodeID: "extract", ViewHash: view.SHA256, RecordIDs: []string{"raw"}, TargetGeneration: 2, TargetPlanHash: plan.Digest, ExpiresAt: store.Now() + 30000}
}
func publishImport(t *testing.T, h *jobHarness, j *Job, spec graph.Spec, ev GraphEvaluation) (*Job, error) {
	t.Helper()
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		return nil, e
	}
	plan, e := graph.Compile(spec)
	if e != nil {
		return nil, e
	}
	in := replanProposal(st.GoalID, st.EvaluationID, h.spec("single"))
	in.ProposedPlan.JobSpecHash = plan.Digest
	in.Evidence = goal.ReplanEvidence{FailureClass: goal.FailureTest, Evidence: []goal.Evidence{{Type: "iu_graph_evaluation", Fact: graphEvaluationFact(ev)}}}
	next, decision, e := h.s.PublishGoalReplan(h.ctx, j.ID, in, "", job.JSON(spec))
	if e == nil && !decision.Allowed {
		t.Fatalf("rejected %+v", decision)
	}
	return next, e
}
func TestImportRechecksRevokedGrantAtCommit(t *testing.T) {
	h, j, spec, ev := importSource(t)
	in := makeImportGrant(t, h, j, spec)
	if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	if e := h.s.RevokeGraphImport(h.ctx, j.ID, in.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := publishImport(t, h, j, spec, ev); e == nil {
		t.Fatal("revoked grant consumed")
	}
	var generation int64
	h.s.db.SQL.QueryRow(`SELECT active_graph_generation FROM goals WHERE id=?`, j.graph.GoalID).Scan(&generation)
	if generation != 1 {
		t.Fatal("publication did not roll back")
	}
}
func TestImportFreezesSnapshotAndPinsOrigin(t *testing.T) {
	h, j, spec, ev := importSource(t)
	in := makeImportGrant(t, h, j, spec)
	if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	next, e := publishImport(t, h, j, spec, ev)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.RevokeGraphImport(h.ctx, j.ID, in.ID); e == nil {
		t.Fatal("consumed grant revoked")
	}
	if e = h.s.advanceGraph(h.ctx, next.ID); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e = h.s.db.SQL.QueryRow(`SELECT input_view FROM iu_nodes WHERE goal_id=? AND generation=2 AND node_id='extract'`, j.graph.GoalID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var v graph.MemoryView
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	if len(v.Records) != 1 || v.Records[0].ID == "raw" || v.Records[0].Validation != "unchecked" {
		t.Fatalf("bad import %+v", v)
	}
	var n int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM artifact_refs WHERE ref_type='iu_memory' AND ref_id LIKE 'import/%'`).Scan(&n)
	if n < 1 {
		t.Fatal("origin unpinned")
	}
	cfg := h.s.cfg
	if e = h.s.Close(); e != nil {
		t.Fatal(e)
	}
	h.s, e = New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := publishImport(t, h, j, spec, ev)
	if e != nil || replay.ID != next.ID {
		t.Fatalf("restart replay %v", e)
	}
	var tasks int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, next.ID).Scan(&tasks)
	if tasks != 1 {
		t.Fatalf("tasks %d", tasks)
	}
}
func TestImportRejectsStaleRevisionAndChangedContext(t *testing.T) {
	for _, mode := range []string{"revision", "instructions", "source", "digest", "cross-goal", "expiry", "issuer-revoked"} {
		t.Run(mode, func(t *testing.T) {
			h, j, spec, ev := importSource(t)
			in := makeImportGrant(t, h, j, spec)
			if mode == "instructions" {
				spec.Nodes[0].Instructions += "changed"
				p, _ := graph.Compile(spec)
				in.TargetPlanHash = p.Digest
			}
			if mode == "source" {
				spec.Sources = []graph.Source{{ID: "new", Text: "discount=2000"}}
				spec.Rules = []graph.Span{{Source: "new", Start: 0, End: 13, SHA256: graph.Hash([]byte("discount=2000"))}}
				p, _ := graph.Compile(spec)
				in.TargetPlanHash = p.Digest
			}
			if mode == "digest" {
				in.TargetPlanHash = strings.Repeat("a", 64)
			}
			if mode == "cross-goal" {
				in.TargetGeneration = 3
				if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e == nil {
					t.Fatal("wrong target issued")
				}
				return
			}
			if mode == "issuer-revoked" {
				key := store.APIKey{ID: "import-key", Digest: store.Hash([]byte("secret")), Prefix: "cc_live_test", Owner: "owner", Projects: []string{"project"}, Scopes: []string{"jobs:read", "goals:import"}, CreatedAt: store.Now(), ExpiresAt: store.Now() + 60000}
				if e := h.s.db.CreateAPIKey(h.ctx, key); e != nil {
					t.Fatal(e)
				}
				p, _ := rpcutil.PrincipalFrom(h.ctx)
				p.ManagedKeyDigest = key.Digest
				h.ctx = rpcutil.WithPrincipal(h.ctx, p)
			}
			if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "revision":
				if e := h.s.InvalidateGraph(h.ctx, j.ID, graph.InvalidationEvent{ID: "late", GoalID: j.graph.GoalID, Generation: 1, Reason: "COUNTEREVIDENCE", EvidenceHash: strings.Repeat("b", 64), Records: []string{"raw"}}); e != nil {
					t.Fatal(e)
				}
			case "expiry":
				h.s.db.SQL.Exec(`UPDATE iu_import_grant_state SET expires_at=0`)
			case "issuer-revoked":
				h.s.db.SQL.Exec(`UPDATE api_keys SET revoked_at=? WHERE id='import-key'`, store.Now())
				p, _ := rpcutil.PrincipalFrom(h.ctx)
				p.ManagedKeyDigest = ""
				h.ctx = rpcutil.WithPrincipal(h.ctx, p)
			}
			if _, e := publishImport(t, h, j, spec, ev); e == nil {
				t.Fatal("incompatible import accepted")
			}
		})
	}
}

func TestImportIdempotencyExpiryAndGCFencing(t *testing.T) {
	h, j, spec, _ := importSource(t)
	in := makeImportGrant(t, h, j, spec)
	first, e := h.s.IssueGraphImport(h.ctx, j.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := h.s.IssueGraphImport(h.ctx, j.ID, in)
	if e != nil || first.State != second.State {
		t.Fatal(e)
	}
	changed := in
	changed.ViewHash = strings.Repeat("f", 64)
	if _, e = h.s.IssueGraphImport(h.ctx, j.ID, changed); e == nil {
		t.Fatal("idempotency collision")
	}
	var artifact string
	if e = h.s.db.SQL.QueryRow(`SELECT artifact FROM artifact_refs WHERE ref_id=?`, importGrantRef(j.graph.GoalID, in.ID)).Scan(&artifact); e != nil {
		t.Fatal(e)
	}
	if _, e = h.s.db.SQL.Exec(`UPDATE artifacts SET state='DELETING' WHERE id=?`, artifact); e == nil {
		t.Fatal("GC deleted pinned evidence")
	}
	if _, e = h.s.db.SQL.Exec(`DELETE FROM artifact_refs WHERE ref_id=?`, importGrantRef(j.graph.GoalID, in.ID)); e == nil {
		t.Fatal("live pin removed")
	}
	if _, e = h.s.db.SQL.Exec(`UPDATE iu_import_grant_state SET expires_at=0`); e != nil {
		t.Fatal(e)
	}
	if e = h.s.expireGraphImports(h.ctx); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM artifact_refs WHERE ref_id=?`, importGrantRef(j.graph.GoalID, in.ID)).Scan(&n); e != nil || n != 0 {
		t.Fatalf("expired pin %d %v", n, e)
	}
	if _, e = h.s.db.SQL.Exec(`UPDATE iu_import_grants SET actor='forged'`); e == nil {
		t.Fatal("grant mutated")
	}
}
func TestImportRequiresExplicitAuthorityAndGuard(t *testing.T) {
	h, j, spec, _ := importSource(t)
	in := makeImportGrant(t, h, j, spec)
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.Identity.Scopes = []string{"jobs:read", "jobs:submit"}
	ctx := rpcutil.WithPrincipal(h.ctx, p)
	if _, e := h.s.IssueGraphImport(ctx, j.ID, in); e == nil {
		t.Fatal("submitter granted imports")
	}
	if _, e := h.s.SubmitGraph(h.ctx, "new-goal-import", job.JSON(spec)); e == nil {
		t.Fatal("new Goal imported history")
	}
}
