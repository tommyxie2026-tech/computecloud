package graph

import (
	"os"
	"testing"
)

func TestOrdersExampleCompilesWithFrozenSchemasAndSources(t *testing.T) {
	raw, e := os.ReadFile("../../examples/iu-graph/orders.json")
	if e != nil {
		t.Fatal(e)
	}
	s, e := Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	p, e := Compile(s)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Order) != 5 || p.EffectiveParallelism != 2 {
		t.Fatalf("%+v", p)
	}
	for _, node := range s.Nodes {
		op := Operator{}
		for _, candidate := range s.Operators {
			if candidate.ID == node.Operator {
				op = candidate
			}
		}
		if _, e = CompileContext(ContextRequest{Sources: s.Sources, Rules: s.Rules, Spans: node.Spans, Instructions: node.Instructions, View: EmptyView(), Model: "example", OutputSchema: OutputSchema(op.Outputs, s.Schemas)}); e != nil {
			t.Fatalf("%s: %v", node.ID, e)
		}
	}
}
