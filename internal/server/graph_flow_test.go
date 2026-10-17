package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func TestGraphTwoWorkerMemoryJoinAndTrustedCommandFlow(t *testing.T) {
	verifier := filepath.Join(t.TempDir(), "verify-orders")
	program, e := os.ReadFile("../../examples/iu-graph/verify_orders.py")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(verifier, program, 0700); e != nil {
		t.Fatal(e)
	}
	binding := job.Execution{RuntimeProfile: "command_exec", Model: "verify", CredentialRef: "account", PolicyRef: "review", AcceptanceProfile: "check"}
	h := newJobHarnessWithWorkerConfig(t, true, nil, func(c *config.Worker) {
		c.Runtimes["command_exec"] = config.Runtime{Executable: verifier, Version: "iu-orders-verifier-1", Models: []string{"verify"}, Credentials: []string{"account"}}
	}, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate")
		c.Jobs.GraphCommands = map[string]config.GraphCommandProfile{"verify": {Execution: binding, ParametersSchema: `{"type":"object","additionalProperties":false}`}}
	})
	s := graphSpec(h)
	s.Schemas = map[string]json.RawMessage{"amount-v1": json.RawMessage(`{"type":"integer"}`), "amounts-v1": json.RawMessage(`{"type":"object","additionalProperties":{"type":"integer"}}`), "verdict-v1": json.RawMessage(`{"enum":["PASS","FAIL","UNKNOWN"]}`), "optional-amount-v1": json.RawMessage(`{"type":["integer","null"]}`)}
	ex := *s.Operators[0].Execution
	a := graph.Source{ID: "order_a", Text: `{"order_id":"a","customer_id":"c","paid_cents":6000}`}
	a.SHA256 = graph.Hash([]byte(a.Text))
	b := a
	b.ID = "order_b"
	b.Text = `{"order_id":"b","customer_id":"c","paid_cents":6000}`
	b.SHA256 = graph.Hash([]byte(b.Text))
	rule := graph.Source{ID: "discount", Text: `{"threshold_cents":10000,"discount_cents":1000}`}
	rule.SHA256 = graph.Hash([]byte(rule.Text))
	s.Sources = []graph.Source{a, b, rule}
	s.Rules = []graph.Span{{Source: rule.ID, Start: 0, End: len(rule.Text), SHA256: rule.SHA256}}
	s.Operators = []graph.Operator{
		{ID: "extract", Version: "1", Kind: "agent", Execution: &ex, Outputs: []graph.Port{{Name: "result", Schema: "amount-v1"}}},
		{ID: "join", Version: "1", Kind: "join", Inputs: []graph.Port{{Name: "a", Schema: "amount-v1"}, {Name: "b", Schema: "amount-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "amounts-v1"}}},
		{ID: "discount", Version: "1", Kind: "agent", Execution: &ex, Inputs: []graph.Port{{Name: "amounts", Schema: "amounts-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "amount-v1"}}},
		{ID: "verify", Version: "1", Kind: "command", CommandProfile: "verify", Inputs: []graph.Port{{Name: "amount", Schema: "amount-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "verdict-v1"}, {Name: "expected_total", Schema: "optional-amount-v1"}}},
	}
	s.Nodes = []graph.Node{{ID: "a", Operator: "extract", Instructions: "fixture-amount:6000 fixture-record:order_a", Spans: []graph.Span{{Source: a.ID, Start: 0, End: len(a.Text), SHA256: a.SHA256}}}, {ID: "b", Operator: "extract", Instructions: "fixture-amount:6000 fixture-record:order_b", Spans: []graph.Span{{Source: b.ID, Start: 0, End: len(b.Text), SHA256: b.SHA256}}}, {ID: "join", Operator: "join", JoinPolicy: "all_success"}, {ID: "discount", Operator: "discount", Instructions: "fixture-discount:1000 fixture-record:total"}, {ID: "verify", Operator: "verify", Parameters: json.RawMessage(`{}`), Spans: []graph.Span{{Source: a.ID, Start: 0, End: len(a.Text), SHA256: a.SHA256}, {Source: b.ID, Start: 0, End: len(b.Text), SHA256: b.SHA256}}}}
	s.Edges = []graph.Edge{{FromNode: "a", FromPort: "result", ToNode: "join", ToPort: "a", Type: "memory"}, {FromNode: "b", FromPort: "result", ToNode: "join", ToPort: "b", Type: "memory"}, {FromNode: "join", FromPort: "result", ToNode: "discount", ToPort: "amounts", Type: "memory"}, {FromNode: "discount", FromPort: "result", ToNode: "verify", ToPort: "amount", Type: "memory"}}
	s.Outputs = []graph.Output{{Node: "discount", Port: "result"}, {Node: "verify", Port: "result"}}
	j, e := h.s.SubmitGraph(h.ctx, "orders-flow", job.JSON(s))
	if e != nil {
		t.Fatal(e)
	}
	j = h.wait(t, j.ID)
	if j.State != "SUCCEEDED" {
		t.Fatalf("graph failed: %+v", j)
	}
	var result struct {
		Outputs map[string]json.RawMessage `json:"outputs"`
	}
	if e = json.Unmarshal(j.result, &result); e != nil {
		t.Fatal(e)
	}
	if string(result.Outputs["discount/result"]) != "11000" || string(result.Outputs["verify/result"]) != `"PASS"` {
		t.Fatalf("global discount lost: %s", j.result)
	}
	var viewJSON []byte
	if e = h.s.db.SQL.QueryRow(`SELECT output_view FROM iu_nodes WHERE goal_id=? AND generation=1 AND node_id='verify'`, j.graph.GoalID).Scan(&viewJSON); e != nil {
		t.Fatal(e)
	}
	var view graph.MemoryView
	if e = json.Unmarshal(viewJSON, &view); e != nil || len(view.Records) != 3 {
		t.Fatalf("memory=%s %v", viewJSON, e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil || st.GoalState != "EVALUATING" {
		t.Fatalf("goal completed before business acceptance %+v %v", st, e)
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, GraphEvaluation{ID: "orders-pass", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: graph.Hash(j.result)}); e != nil {
		t.Fatal(e)
	}
}
