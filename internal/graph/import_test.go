package graph

import (
	"encoding/json"
	"testing"
)

func importFixture() MemoryView {
	return NewView([]MemoryRecord{
		{ID: "raw", Kind: "observation", Payload: json.RawMessage(`{"amount":12000}`), Sources: []SourceRef{{ID: "orders", SHA256: Hash([]byte("orders"))}}, Validity: "VALID", Validation: "verified"},
		{ID: "total", Kind: "result", Payload: json.RawMessage(`11000`), Dependencies: []string{"raw"}, Validity: "VALID", Validation: "unchecked"},
	})
}
func TestImportRejectsInvalidOrIncompleteClosure(t *testing.T) {
	v := importFixture()
	for _, ids := range [][]string{{"total"}, {"missing"}, {"raw", "raw"}, nil} {
		if _, e := ImportView(v, ids, "grant_a"); e == nil {
			t.Fatalf("accepted %v", ids)
		}
	}
	for _, state := range []string{"INVALID", "NEEDS_REVALIDATION"} {
		records := v.Records
		records[0].Validity = state
		if _, e := ImportView(NewView(records), []string{"raw", "total"}, "grant_a"); e == nil {
			t.Fatal(state)
		}
	}
	v = importFixture()
	v.Conflicts = []MemoryConflict{{RecordID: "raw", Candidates: []MemoryRecord{v.Records[0]}}}
	v = sealView(v)
	if _, e := ImportView(v, []string{"raw"}, "grant_a"); e == nil {
		t.Fatal("conflict imported")
	}
}
func TestImportPreservesOriginAndNamespace(t *testing.T) {
	v := importFixture()
	before := string(canonical(v))
	a, e := ImportView(v, []string{"total", "raw"}, "grant_a")
	if e != nil {
		t.Fatal(e)
	}
	b, e := ImportView(v, []string{"raw", "total"}, "grant_b")
	if e != nil {
		t.Fatal(e)
	}
	if a.View.SHA256 == b.View.SHA256 || a.IDs["raw"] == "raw" || a.IDs["raw"] == b.IDs["raw"] {
		t.Fatal("namespace collision")
	}
	if a.OriginViewHash != v.SHA256 || string(canonical(v)) != before {
		t.Fatal("origin changed")
	}
	for _, r := range a.View.Records {
		if r.Validation != "unchecked" {
			t.Fatal("promoted verification")
		}
		if r.ID == a.IDs["total"] && (len(r.Dependencies) != 1 || r.Dependencies[0] != a.IDs["raw"]) {
			t.Fatal("lost lineage")
		}
	}
	again, e := ImportView(v, []string{"raw", "total"}, "grant_a")
	if e != nil || again.View.SHA256 != a.View.SHA256 {
		t.Fatal("unstable import")
	}
}

func TestImportGraphVersionAndCanonicalBindings(t *testing.T) {
	s := fixture()
	s.Imports = []ImportBinding{{GrantID: "grant_a", NodeIDs: []string{"b", "a"}}}
	if _, e := Compile(s); e == nil {
		t.Fatal("v1 accepted imports")
	}
	s.Version = ImportVersion
	p, e := Compile(s)
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := Decode(canonical(p.Spec))
	if e != nil || len(decoded.Imports) != 1 {
		t.Fatal(e)
	}
	s.Imports[0].NodeIDs = []string{"a", "b"}
	p2, e := Compile(s)
	if e != nil || p2.Digest != p.Digest {
		t.Fatal("unstable binding", e)
	}
	s.Imports[0].NodeIDs = []string{"absent"}
	if _, e = Compile(s); e == nil {
		t.Fatal("absent target")
	}
}
