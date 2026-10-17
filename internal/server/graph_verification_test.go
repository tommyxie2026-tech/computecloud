package server

import (
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"strings"
	"testing"
)

func verificationSource(t *testing.T) (*jobHarness, *Job, GraphRecordVerificationRequest) {
	t.Helper()
	h := newOfflineGraphHarness(t)
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.Identity.Scopes = append(p.Identity.Scopes, "goals:verify")
	h.ctx = rpcutil.WithPrincipal(h.ctx, p)
	h.s.cfg.Jobs.GraphRecordVerifiers = map[string]graph.RecordVerifierProfile{"order": {Version: "1", Kind: "source_json_equal", Scope: "source-paid", SourcePointer: "/paid_cents", ClaimPointer: "/value", RecordKinds: []string{"observation"}, TimeoutMS: 100}}
	spec := graphSpec(h)
	src := graph.Source{ID: "order", Text: `{"paid_cents":6000}`}
	src.SHA256 = graph.Hash([]byte(src.Text))
	spec.Sources = []graph.Source{src}
	spec.Nodes[0].Spans = []graph.Span{{Source: src.ID, Start: 0, End: len(src.Text), SHA256: src.SHA256}}
	j, e := h.s.SubmitGraph(h.ctx, "verify", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	var out graph.ExecutionOutput
	json.Unmarshal(graphCandidateOutput(t, h, j), &out)
	out.Delta.Records = []graph.MemoryRecord{{ID: "raw", Kind: "observation", Payload: json.RawMessage(`{"value":6000}`), Sources: []graph.SourceRef{{ID: src.ID, SHA256: src.SHA256}}, Validity: "VALID", Validation: "unchecked"}, {ID: "other", Kind: "observation", Payload: json.RawMessage(`{"value":1}`), Sources: []graph.SourceRef{{ID: src.ID, SHA256: src.SHA256}}, Validity: "VALID", Validation: "unchecked"}}
	seedGraphCandidate(t, h, j, job.JSON(out))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	view, e := h.s.GraphMemory(h.ctx, j.ID, "extract")
	if e != nil {
		t.Fatal(e)
	}
	var record graph.MemoryRecord
	for _, r := range view.Records {
		if r.ID == "raw" {
			record = r
		}
	}
	return h, j, GraphRecordVerificationRequest{ID: "verify_raw", NodeID: "extract", RecordID: "raw", RecordHash: graph.Hash(job.JSON(record)), ViewHash: view.SHA256, Profile: "order", ProfileVersion: "1", SourceID: "order"}
}
func TestVerificationCannotPromoteUncoveredRecords(t *testing.T) {
	h, j, in := verificationSource(t)
	receipt, e := h.s.VerifyGraphRecord(h.ctx, j.ID, in)
	if e != nil || receipt.Verdict != "PASS" {
		t.Fatal(e, receipt)
	}
	v, e := h.s.VerifiedGraphMemory(h.ctx, j.ID, "extract", "source-paid")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range v.View.Records {
		want := "unchecked"
		if r.ID == "raw" {
			want = "verified"
		}
		if r.Validation != want {
			t.Fatalf("record %s got %s", r.ID, r.Validation)
		}
	}
	original, e := h.s.GraphMemory(h.ctx, j.ID, "extract")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range original.Records {
		if r.Validation != "unchecked" {
			t.Fatal("history changed")
		}
	}
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.Identity.Scopes = []string{"jobs:read"}
	if _, e = h.s.VerifyGraphRecord(rpcutil.WithPrincipal(h.ctx, p), j.ID, in); e == nil {
		t.Fatal("unauthorized verification")
	}
	in.RecordID = "other"
	if _, e = h.s.VerifyGraphRecord(h.ctx, j.ID, in); e == nil {
		t.Fatal("same proof promoted another record")
	}
	var refs int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM artifact_refs WHERE ref_id LIKE 'verification/%'`).Scan(&refs)
	if refs < 1 {
		t.Fatal("verification evidence unpinned")
	}
}
func TestVerificationRejectsStaleEvidence(t *testing.T) {
	h, j, in := verificationSource(t)
	if _, e := h.s.VerifyGraphRecord(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	ev := graph.InvalidationEvent{ID: "counterproof", GoalID: j.graph.GoalID, Generation: 1, Reason: "COUNTEREVIDENCE", EvidenceHash: strings.Repeat("b", 64), Records: []string{"raw"}}
	if e := h.s.InvalidateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
	if e := h.s.reconcileGraphInvalidations(h.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := h.s.VerifyGraphRecord(h.ctx, j.ID, in); e == nil {
		t.Fatal("stale evidence accepted")
	}
	v, e := h.s.VerifiedGraphMemory(h.ctx, j.ID, "extract", "source-paid")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range v.View.Records {
		if r.Validation == "verified" {
			t.Fatal("invalid proof still verified")
		}
	}
}
func TestVerificationRechecksManagedKeyAndProfileChanges(t *testing.T) {
	h, j, in := verificationSource(t)
	key := store.APIKey{ID: "verify-key", Digest: store.Hash([]byte("secret")), Prefix: "cc_live_verify", Owner: "owner", Projects: []string{"project"}, Scopes: []string{"jobs:read", "goals:verify"}, CreatedAt: store.Now(), ExpiresAt: store.Now() + 60000}
	if e := h.s.db.CreateAPIKey(h.ctx, key); e != nil {
		t.Fatal(e)
	}
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.ManagedKeyDigest = key.Digest
	ctx := rpcutil.WithPrincipal(h.ctx, p)
	if _, e := h.s.db.SQL.Exec(`UPDATE api_keys SET revoked_at=? WHERE id=?`, store.Now(), key.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := h.s.VerifyGraphRecord(ctx, j.ID, in); e == nil {
		t.Fatal("revoked key verified")
	}
	if _, e := h.s.VerifyGraphRecord(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	profile := h.s.cfg.Jobs.GraphRecordVerifiers["order"]
	profile.Version = "2"
	h.s.cfg.Jobs.GraphRecordVerifiers["order"] = profile
	v, e := h.s.VerifiedGraphMemory(h.ctx, j.ID, "extract", "source-paid")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range v.View.Records {
		if r.Validation == "verified" {
			t.Fatal("changed profile reused proof")
		}
	}
}
func TestVerificationEvaluationReferencesCannotBeForged(t *testing.T) {
	h, j, in := verificationSource(t)
	receipt, e := h.s.VerifyGraphRecord(h.ctx, j.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	ev := GraphEvaluation{ID: "with-proof", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: st.OutputHash, RecordProofs: []GraphRecordProofRef{{NodeID: "extract", ID: in.ID, Hash: strings.Repeat("f", 64), Scope: receipt.Scope}}}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e == nil {
		t.Fatal("forged receipt linked")
	}
	ev.RecordProofs[0].Hash = graph.Hash(job.JSON(receipt))
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
}
