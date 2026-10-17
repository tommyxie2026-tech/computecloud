package graph

import (
	"context"
	"encoding/json"
	"testing"
)

func TestVerifierTimeoutRemainsUnknown(t *testing.T) {
	p := RecordVerifierProfile{Version: "1", Kind: "source_json_equal", Scope: "paid_cents", SourcePointer: "/paid_cents", ClaimPointer: "/value", RecordKinds: []string{"observation"}, TimeoutMS: 100}
	src := Source{ID: "order", Text: `{"paid_cents":6000}`}
	src.SHA256 = Hash([]byte(src.Text))
	r := MemoryRecord{ID: "raw", Kind: "observation", Payload: json.RawMessage(`{"value":6000}`), Validity: "VALID", Validation: "unchecked"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := RunRecordVerifier(ctx, p, src, r); out.Verdict != "UNKNOWN" {
		t.Fatal("timeout passed")
	}
	if out := RunRecordVerifier(context.Background(), p, src, r); out.Verdict != "PASS" {
		t.Fatal(out)
	}
	r.Payload = json.RawMessage(`{"value":11000}`)
	if out := RunRecordVerifier(context.Background(), p, src, r); out.Verdict != "FAIL" {
		t.Fatal(out)
	}
	src.Text = `{"paid_cents":6000,"paid_cents":11000}`
	src.SHA256 = Hash([]byte(src.Text))
	if out := RunRecordVerifier(context.Background(), p, src, r); out.Verdict != "UNKNOWN" {
		t.Fatal("ambiguous source accepted")
	}
}
func TestEmpiricalContractDoesNotInventCertainty(t *testing.T) {
	p := EmpiricalEvidence{EvaluationProfile: "eval", EvaluationVersion: "1", DatasetHash: Hash([]byte("dataset"))}
	if e := p.Calibrate(); e != nil || p.Lower95 != nil {
		t.Fatal("unknown samples became confidence")
	}
	p.Samples = 20
	p.Successes = 20
	if e := p.Calibrate(); e != nil || p.Lower95 == nil || *p.Lower95 >= 1 {
		t.Fatal("empirical result became certainty")
	}
	p.Successes = 21
	if e := p.Calibrate(); e == nil {
		t.Fatal("bad denominator")
	}
}
func TestVerificationProjectionRetainsConflictEvidence(t *testing.T) {
	a := NewView([]MemoryRecord{{ID: "conflict", Kind: "observation", Payload: json.RawMessage(`1`), Validation: "unchecked", Validity: "VALID"}})
	b := NewView([]MemoryRecord{{ID: "conflict", Kind: "observation", Payload: json.RawMessage(`2`), Validation: "unchecked", Validity: "VALID"}})
	merged, e := MergeViews([]MemoryView{a, b})
	if e != nil {
		t.Fatal(e)
	}
	projected, e := ProjectVerifiedScope(merged, nil)
	if e != nil || len(projected.Conflicts) != 1 || len(projected.Conflicts[0].Candidates) != 2 {
		t.Fatal("lost conflict evidence", e)
	}
	if _, e = ProjectVerifiedScope(merged, []string{"conflict"}); e == nil {
		t.Fatal("conflict verified")
	}
}
