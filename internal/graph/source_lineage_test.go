package graph

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestOutputBindsAllFrozenSourcesWithoutTrustingModelClaims(t *testing.T) {
	sources := []Source{{ID: "order", Text: "6000"}, {ID: "rule", Text: "1000"}}
	spans := []Span{}
	for i := range sources {
		sources[i].SHA256 = Hash([]byte(sources[i].Text))
		spans = append(spans, Span{Source: sources[i].ID, Start: 0, End: len(sources[i].Text), SHA256: sources[i].SHA256})
	}
	m, e := CompileContext(ContextRequest{Sources: sources, Spans: spans, Model: "m", View: EmptyView()})
	if e != nil {
		t.Fatal(e)
	}
	inv := Invocation{Context: m, View: EmptyView(), Outputs: []Port{{Name: "result", Schema: "any"}}, Schemas: map[string]json.RawMessage{"any": json.RawMessage(`{}`)}}
	out, e := BuildOutput([]byte(`{"version":"iu-node-output-v1","values":{"result":1},"records":[{"id":"claim","kind":"result","payload":1,"validation":"unchecked","validity":"VALID"}]}`), inv)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Delta.Records[0].Sources) != 2 {
		t.Fatal("model omitted a governing source")
	}
}
func TestFrozenSourceLimitFailsInsteadOfDroppingProvenance(t *testing.T) {
	spans := []Span{}
	for i := 0; i < 65; i++ {
		spans = append(spans, Span{Source: fmt.Sprintf("source_%d", i), SHA256: Hash([]byte("x")), Start: 0, End: 1})
	}
	inv := Invocation{Context: ContextManifest{SHA256: Hash([]byte("ctx")), Spans: spans}, View: EmptyView(), Outputs: []Port{{Name: "result", Schema: "any"}}, Schemas: map[string]json.RawMessage{"any": json.RawMessage(`{}`)}}
	if _, e := BuildOutput([]byte(`{"version":"iu-node-output-v1","values":{"result":1},"records":[{"id":"claim","kind":"result","payload":1,"validation":"unchecked","validity":"VALID"}]}`), inv); e == nil {
		t.Fatal("provenance silently omitted")
	}
}

func TestSourceBindingRejectsMalformedCandidateBeforeUnion(t *testing.T) {
	h := Hash([]byte("x"))
	d := Delta{Records: []MemoryRecord{{ID: "claim", Kind: "result", Payload: json.RawMessage(`1`), Validation: "unchecked", Validity: "VALID", Sources: []SourceRef{{ID: "source", SHA256: h}, {ID: "source", SHA256: h}}}}}
	if _, e := BindDeltaSources(d, nil); e == nil {
		t.Fatal("duplicate candidate sources were laundered")
	}
}
