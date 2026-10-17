package graph

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceInvalidationProjectsTwoHopsWithoutChangingHistory(t *testing.T) {
	src := SourceRef{ID: "rule", SHA256: strings.Repeat("a", 64)}
	v := NewView([]MemoryRecord{
		{ID: "rule", Kind: "constraint", Payload: json.RawMessage(`1`), Sources: []SourceRef{src}, Validation: "unchecked", Validity: "VALID"},
		{ID: "subtotal", Kind: "result", Payload: json.RawMessage(`2`), Dependencies: []string{"rule"}, Validation: "unchecked", Validity: "VALID"},
		{ID: "total", Kind: "result", Payload: json.RawMessage(`3`), Dependencies: []string{"subtotal"}, Validation: "unchecked", Validity: "VALID"},
	})
	projected, e := ProjectInvalidation(v, InvalidationEvent{Sources: []SourceRef{src}})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range projected.Records {
		if r.Validity != "INVALID" {
			t.Fatalf("not invalidated: %+v", r)
		}
	}
	if v.Records[0].Validity != "VALID" || v.SHA256 == projected.SHA256 {
		t.Fatal("history mutated")
	}
	unchanged, e := ProjectInvalidation(v, InvalidationEvent{Sources: []SourceRef{{ID: "rule", SHA256: strings.Repeat("b", 64)}}})
	if e != nil || unchanged.SHA256 != v.SHA256 {
		t.Fatal("other source version invalidated")
	}
}
