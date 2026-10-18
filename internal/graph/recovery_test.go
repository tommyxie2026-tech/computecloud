package graph

import (
	"encoding/json"
	"testing"
)

func cloneCoverage(s Spec) Spec { var out Spec; json.Unmarshal(canonical(s), &out); return out }
func TestRecoveryRejectsUnauthorizedDiffAndOrdinaryReplanBypass(t *testing.T) {
	s := coverageFixture()
	next := cloneCoverage(s)
	next.Nodes[0].Instructions += "\nread the evidence"
	if e := ValidateRecovery(s, next); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Spec){func(n *Spec) { n.Operators[0].Execution.Model = "other" }, func(n *Spec) { n.Nodes[0].Spans = nil }, func(n *Spec) { n.Limits.MaxReplans++ }, func(n *Spec) { n.Nodes[0].Instructions = "replace" }} {
		n := cloneCoverage(next)
		change(&n)
		if e := ValidateRecovery(s, n); e == nil {
			t.Fatal("unrestricted change")
		}
	}
	if e := ValidateRecovery(s, s); e == nil {
		t.Fatal("no change accepted")
	}
}
