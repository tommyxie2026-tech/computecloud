package graph

import (
	"encoding/json"
	"testing"
)

func TestInvalidationRetainsConflictHistory(t *testing.T) {
	a := NewView([]MemoryRecord{{ID: "same", Kind: "observation", Payload: json.RawMessage(`1`), Validation: "unchecked", Validity: "VALID"}})
	b := NewView([]MemoryRecord{{ID: "same", Kind: "observation", Payload: json.RawMessage(`2`), Validation: "unchecked", Validity: "VALID"}})
	v, e := MergeViews([]MemoryView{a, b})
	if e != nil {
		t.Fatal(e)
	}
	_, e = Invalidate(v, []string{"same"})
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateView(v); e != nil {
		t.Fatalf("original view invalidated by mutation: %v; candidate=%s", e, v.Conflicts[0].Candidates[0].Validity)
	}
}
