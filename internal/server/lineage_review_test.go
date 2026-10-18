package server

import (
	"encoding/json"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
)

func TestInvalidationArtifactDependenciesRemainUncertain(t *testing.T) {
	h := newOfflineGraphHarness(t)
	spec := graphSpec(h)
	spec.Sources = []graph.Source{{ID: "rule", Text: "old"}}
	spec.Nodes[0].Spans = []graph.Span{{Source: "rule", Start: 0, End: 3, SHA256: graph.Hash([]byte("old"))}}
	op := spec.Operators[0]
	op.ID = "derive"
	op.Inputs = []graph.Port{{Name: "data", Schema: "report-v1"}}
	spec.Operators = append(spec.Operators, op)
	spec.Nodes = append(spec.Nodes, graph.Node{ID: "end", Operator: "derive"})
	spec.Edges = []graph.Edge{{FromNode: "extract", FromPort: "result", ToNode: "end", ToPort: "data", Type: "artifact"}}
	spec.Outputs = []graph.Output{{Node: "end", Port: "result"}}
	j, e := h.s.SubmitGraph(h.ctx, "artifact-lineage", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range []string{"extract", "end"} {
		var out graph.ExecutionOutput
		if e = json.Unmarshal(graphNodeCandidateOutput(t, h, j, node), &out); e != nil {
			t.Fatal(e)
		}
		out.Delta.Records = []graph.MemoryRecord{{ID: node, Kind: "result", Payload: json.RawMessage(`1`), Validation: "unchecked", Validity: "VALID"}}
		seedGraphNodeCandidate(t, h, j, node, job.JSON(out))
		if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
			t.Fatal(e)
		}
	}
	e = h.s.InvalidateGraph(h.ctx, j.ID, graph.InvalidationEvent{ID: "changed", GoalID: j.graph.GoalID, Generation: 1, Reason: "SOURCE_REVISED", EvidenceHash: graph.Hash([]byte("new")), Sources: []graph.SourceRef{{ID: "rule", SHA256: graph.Hash([]byte("old"))}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.reconcileGraphInvalidations(h.ctx); e != nil {
		t.Fatal(e)
	}
	v, e := h.s.GraphMemory(h.ctx, j.ID, "end")
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Records) != 1 || v.Records[0].Validity != "NEEDS_REVALIDATION" {
		t.Fatalf("artifact provenance must remain uncertain: %+v", v.Records)
	}
}
func TestImportRejectsSourceRoleAndOrderChange(t *testing.T) {
	for _, change := range []string{"role", "order"} {
		t.Run(change, func(t *testing.T) {
			h := newOfflineGraphHarness(t)
			p, _ := rpcutil.PrincipalFrom(h.ctx)
			p.Identity.Scopes = append(p.Identity.Scopes, "goals:import")
			h.ctx = rpcutil.WithPrincipal(h.ctx, p)
			spec := graphSpec(h)
			spec.Limits.MaxReplans = 1
			spec.Sources = []graph.Source{{ID: "rule", Text: "old", Role: "system", MessageIndex: 0}}
			spec.Nodes[0].Spans = []graph.Span{{Source: "rule", Start: 0, End: 3, SHA256: graph.Hash([]byte("old"))}}
			j, e := h.s.SubmitGraph(h.ctx, "role-source", job.JSON(spec))
			if e != nil {
				t.Fatal(e)
			}
			var out graph.ExecutionOutput
			json.Unmarshal(graphCandidateOutput(t, h, j), &out)
			out.Delta.Records = []graph.MemoryRecord{{ID: "raw", Kind: "result", Payload: json.RawMessage(`1`), Validation: "unchecked", Validity: "VALID"}}
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
			ev := GraphEvaluation{ID: "fail", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: st.OutputHash}
			if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
				t.Fatal(e)
			}
			spec.Version = graph.ImportVersion
			spec.Imports = []graph.ImportBinding{{GrantID: "grant_a", NodeIDs: []string{"extract"}}}
			if change == "role" {
				spec.Sources[0].Role = "user"
			} else {
				spec.Sources[0].MessageIndex = 1
			}
			in := makeImportGrant(t, h, j, spec)
			if _, e = h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
				t.Fatal(e)
			}
			if next, e := publishImport(t, h, j, spec, ev); e == nil {
				t.Fatalf("import with changed role/order accepted: %s", next.ID)
			}
		})
	}
}
