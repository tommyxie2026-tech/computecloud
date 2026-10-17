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
	program := `#!/usr/bin/env python3
import json,sys
if sys.argv[1:] == ['--version']:
    print('verify-1');sys.exit(0)
context=json.load(sys.stdin)
amount=json.loads(context['inputs'][0]['value'])
verdict='PASS' if amount == context['parameters']['expected'] else 'FAIL'
print(json.dumps({'version':'iu-node-output-v1','values':{'result':verdict},'records':[]}))
`
	if e := os.WriteFile(verifier, []byte(program), 0700); e != nil {
		t.Fatal(e)
	}
	binding := job.Execution{RuntimeProfile: "command_exec", Model: "verify", CredentialRef: "account", PolicyRef: "review", AcceptanceProfile: "check"}
	h := newJobHarnessWithWorkerConfig(t, true, nil, func(c *config.Worker) {
		c.Runtimes["command_exec"] = config.Runtime{Executable: verifier, Version: "verify-1", Models: []string{"verify"}, Credentials: []string{"account"}}
	}, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate")
		c.Jobs.GraphCommands = map[string]config.GraphCommandProfile{"verify": {Execution: binding, ParametersSchema: `{"type":"object","properties":{"expected":{"type":"integer"}},"required":["expected"],"additionalProperties":false}`}}
	})
	s := graphSpec(h)
	s.Schemas = map[string]json.RawMessage{"amount-v1": json.RawMessage(`{"type":"integer"}`), "amounts-v1": json.RawMessage(`{"type":"object","additionalProperties":{"type":"integer"}}`), "verdict-v1": json.RawMessage(`{"enum":["PASS","FAIL","UNKNOWN"]}`)}
	ex := *s.Operators[0].Execution
	a := graph.Source{ID: "order_a", Text: "6000"}
	a.SHA256 = graph.Hash([]byte(a.Text))
	b := a
	b.ID = "order_b"
	rule := graph.Source{ID: "discount", Text: "同一客户两笔订单累计满10000分，只减1000分一次。"}
	rule.SHA256 = graph.Hash([]byte(rule.Text))
	s.Sources = []graph.Source{a, b, rule}
	s.Rules = []graph.Span{{Source: rule.ID, Start: 0, End: len(rule.Text), SHA256: rule.SHA256}}
	s.Operators = []graph.Operator{
		{ID: "extract", Version: "1", Kind: "agent", Execution: &ex, Outputs: []graph.Port{{Name: "result", Schema: "amount-v1"}}},
		{ID: "join", Version: "1", Kind: "join", Inputs: []graph.Port{{Name: "a", Schema: "amount-v1"}, {Name: "b", Schema: "amount-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "amounts-v1"}}},
		{ID: "discount", Version: "1", Kind: "agent", Execution: &ex, Inputs: []graph.Port{{Name: "amounts", Schema: "amounts-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "amount-v1"}}},
		{ID: "verify", Version: "1", Kind: "command", CommandProfile: "verify", Inputs: []graph.Port{{Name: "amount", Schema: "amount-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "verdict-v1"}}},
	}
	s.Nodes = []graph.Node{{ID: "a", Operator: "extract", Instructions: "fixture-amount:6000 fixture-record:order_a", Spans: []graph.Span{{Source: a.ID, Start: 0, End: 4, SHA256: a.SHA256}}}, {ID: "b", Operator: "extract", Instructions: "fixture-amount:6000 fixture-record:order_b", Spans: []graph.Span{{Source: b.ID, Start: 0, End: 4, SHA256: b.SHA256}}}, {ID: "join", Operator: "join", JoinPolicy: "all_success"}, {ID: "discount", Operator: "discount", Instructions: "fixture-discount:1000 fixture-record:total"}, {ID: "verify", Operator: "verify", Parameters: json.RawMessage(`{"expected":11000}`)}}
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
