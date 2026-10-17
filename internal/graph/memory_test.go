package graph

import (
	"encoding/json"
	"strings"
	"testing"
)

func record(id string, deps ...string) MemoryRecord {
	return MemoryRecord{ID: id, Kind: "result", Payload: json.RawMessage(`{"amount":11000}`), Dependencies: deps, Validation: "unchecked", Validity: "VALID"}
}

func TestMemoryDeltaRequiresFrozenViewAndCannotSelfVerify(t *testing.T) {
	view := EmptyView()
	d := Delta{Version: "iu-memory-delta-v1", ReadViewHash: view.SHA256, ContextHash: strings.Repeat("a", 64), Records: []MemoryRecord{record("order")}}
	if _, e := ValidateDelta(canonical(d), view); e != nil {
		t.Fatal(e)
	}
	d.ReadViewHash = strings.Repeat("b", 64)
	if _, e := ValidateDelta(canonical(d), view); e == nil {
		t.Fatal("stale view accepted")
	}
	d.ReadViewHash = view.SHA256
	d.Records[0].Validation = "verified"
	if _, e := ValidateDelta(canonical(d), view); e == nil {
		t.Fatal("self verification accepted")
	}
	d.Records[0] = record("order")
	d.Records[0].Dependencies = []string{"hidden"}
	if _, e := ValidateDelta(canonical(d), view); e == nil {
		t.Fatal("undeclared dependency accepted")
	}
}

func TestMemoryDeltaLimits(t *testing.T) {
	view := EmptyView()
	d := Delta{Version: "iu-memory-delta-v1", ReadViewHash: view.SHA256, ContextHash: strings.Repeat("a", 64)}
	for i := 0; i < 33; i++ {
		d.Records = append(d.Records, record(strings.Repeat("x", i+1)))
	}
	if _, e := ValidateDelta(canonical(d), view); e == nil {
		t.Fatal("33 records accepted")
	}
	d.Records = d.Records[:1]
	d.Records[0].Payload = canonical(strings.Repeat("x", 8193))
	if _, e := ValidateDelta(canonical(d), view); e == nil {
		t.Fatal("large payload accepted")
	}
}

func TestMemoryMergeConflictsAndDeduplicates(t *testing.T) {
	a := NewView([]MemoryRecord{record("order")})
	b := NewView([]MemoryRecord{record("order")})
	merged, e := MergeViews([]MemoryView{b, a})
	if e != nil || len(merged.Records) != 1 {
		t.Fatalf("duplicate %v %+v", e, merged)
	}
	b.Records[0].Payload = json.RawMessage(`{"amount":12000}`)
	b = NewView(b.Records)
	merged, e = MergeViews([]MemoryView{a, b})
	if e != nil {
		t.Fatal(e)
	}
	if len(merged.Conflicts) != 1 || merged.Records[0].Validity != "NEEDS_REVALIDATION" {
		t.Fatalf("conflict lost %+v", merged)
	}
	reversed, e := MergeViews([]MemoryView{b, a})
	if e != nil || reversed.SHA256 != merged.SHA256 {
		t.Fatalf("merge not deterministic: %v", e)
	}
}

func TestMemoryInvalidationPropagatesWithoutChangingHistory(t *testing.T) {
	view := NewView([]MemoryRecord{record("rule"), record("amount", "rule"), record("summary", "amount")})
	next, e := Invalidate(view, []string{"rule"})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range next.Records {
		if r.Validity != "INVALID" || r.Validation != "unchecked" {
			t.Fatalf("bad invalidation %+v", r)
		}
	}
	for _, r := range view.Records {
		if r.Validity != "VALID" {
			t.Fatal("history changed")
		}
	}
	if view.SHA256 == next.SHA256 {
		t.Fatal("view did not change")
	}
	cyclic := NewView([]MemoryRecord{record("a", "b"), record("b", "a")})
	if _, e = Invalidate(cyclic, []string{"a"}); e == nil {
		t.Fatal("dependency cycle accepted")
	}
}

func TestApplyDeltaPreservesConflictsAndInheritedInvalidity(t *testing.T) {
	a := NewView([]MemoryRecord{record("amount")})
	b := NewView([]MemoryRecord{record("amount")})
	b.Records[0].Payload = json.RawMessage(`12000`)
	b = NewView(b.Records)
	v, e := MergeViews([]MemoryView{a, b})
	if e != nil {
		t.Fatal(e)
	}
	d := Delta{Version: "iu-memory-delta-v1", ReadViewHash: v.SHA256, ContextHash: strings.Repeat("a", 64), Records: []MemoryRecord{record("summary")}}
	next, e := ApplyDelta(v, d)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Conflicts) != 1 || len(next.Conflicts[0].Candidates) != 2 {
		t.Fatalf("lost conflict candidates %+v", next)
	}
	for _, r := range next.Records {
		if r.Validity != "NEEDS_REVALIDATION" {
			t.Fatalf("uncertain input became valid %+v", r)
		}
	}
	d.Records[0].Supersedes = []string{"amount"}
	if _, e = ApplyDelta(v, d); e != nil {
		t.Fatalf("supersedes on conflicted read view: %v", e)
	}
	d.Records[0].Supersedes = nil
	v, e = Invalidate(NewView([]MemoryRecord{record("rule")}), []string{"rule"})
	if e != nil {
		t.Fatal(e)
	}
	d.ReadViewHash = v.SHA256
	next, e = ApplyDelta(v, d)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range next.Records {
		if r.Validity != "INVALID" {
			t.Fatalf("invalid input became valid %+v", r)
		}
	}
}
