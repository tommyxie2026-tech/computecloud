package graph

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func fixture() Spec {
	ex := job.Execution{RuntimeProfile: "claude_http", Model: "small", CredentialRef: "key", PolicyRef: "safe", AcceptanceProfile: "report", ReplaySafe: true}
	return Spec{Evaluator: Evaluator{Profile: "external", Version: "1"}, Version: "iu-graph-v1", ProjectID: "project", Workspace: job.Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("a", 40)},
		Sources:   []Source{{ID: "orders", Text: "订单甲：6000；订单乙：6000"}},
		Operators: []Operator{{ID: "extract", Version: "1", Kind: "agent", Outputs: []Port{{Name: "result", Schema: "order-v1"}}, Execution: &ex}, {ID: "merge", Version: "1", Kind: "join", Inputs: []Port{{Name: "a", Schema: "order-v1"}, {Name: "b", Schema: "order-v1"}}, Outputs: []Port{{Name: "result", Schema: "order-v1"}}}},
		Nodes:     []Node{{ID: "b", Operator: "extract", Instructions: "提取乙"}, {ID: "merge", Operator: "merge", JoinPolicy: "all_success"}, {ID: "a", Operator: "extract", Instructions: "提取甲"}},
		Edges:     []Edge{{FromNode: "a", FromPort: "result", ToNode: "merge", ToPort: "a", Type: "artifact"}, {FromNode: "b", FromPort: "result", ToNode: "merge", ToPort: "b", Type: "artifact"}},
		Outputs:   []Output{{Node: "merge", Port: "result"}}, Limits: Limits{Parallelism: 2, TimeoutSeconds: 60, MaxAttemptsPerTask: 1}}
}

func TestCompileDAG(t *testing.T) {
	s := fixture()
	p, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Order, ",") != "a,b,merge" {
		t.Fatalf("order=%v", p.Order)
	}
	s.Nodes[0], s.Nodes[2] = s.Nodes[2], s.Nodes[0]
	q, err := Compile(s)
	if err != nil || q.Digest != p.Digest {
		t.Fatalf("reordering changed frozen graph: %v", err)
	}
	if p.Spec.Sources[0].Text != "订单甲：6000；订单乙：6000" {
		t.Fatal("source changed")
	}
}

func TestRejectCycleAndPortMismatch(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Spec)
	}{
		{"cycle", func(s *Spec) { s.Edges = append(s.Edges, Edge{FromNode: "merge", ToNode: "a", Type: "control"}) }},
		{"schema", func(s *Spec) { s.Operators[1].Inputs[0].Schema = "wrong" }},
		{"duplicate node", func(s *Spec) { s.Nodes[2].ID = "b" }},
		{"unknown operator", func(s *Spec) { s.Nodes[0].Operator = "missing" }},
		{"bad condition", func(s *Spec) { s.Edges[0].When = "eval()" }},
		{"missing output", func(s *Spec) { s.Outputs[0].Port = "missing" }},
		{"duplicate input", func(s *Spec) { s.Edges[1].ToPort = "a" }},
		{"unbound input", func(s *Spec) { s.Edges = s.Edges[:1] }},
		{"source hash", func(s *Spec) { s.Sources[0].SHA256 = strings.Repeat("0", 64) }},
		{"budget tokens", func(s *Spec) { n := int64(100); s.Limits.MaxTokens = &n }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := fixture()
			c.change(&s)
			if _, e := Compile(s); e == nil {
				t.Fatal("accepted invalid graph")
			}
		})
	}
}

func TestGraphLimitsAndUnknownFields(t *testing.T) {
	s := fixture()
	raw, _ := json.Marshal(s)
	if _, e := Decode(raw); e != nil {
		t.Fatal(e)
	}
	for _, b := range [][]byte{append(raw, []byte(" {}")...), []byte(strings.Replace(string(raw), `"project_id":"project"`, `"project_id":"project","surprise":true`, 1)), []byte(strings.Repeat(" ", 256<<10) + string(raw))} {
		if _, e := Decode(b); e == nil {
			t.Fatal("accepted invalid request")
		}
	}
	s.Limits.Parallelism = 9
	if _, e := Compile(s); e == nil {
		t.Fatal("parallelism overflow")
	}
	s = fixture()
	for i := 0; i < 31; i++ {
		s.Nodes = append(s.Nodes, Node{ID: strings.Repeat("x", i+1), Operator: "extract"})
	}
	if _, e := Compile(s); e == nil {
		t.Fatal("node overflow")
	}
}

func TestDecodeRequiresDeclaredCollections(t *testing.T) {
	raw, _ := json.Marshal(fixture())
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "sources")
	raw, _ = json.Marshal(fields)
	if _, err := Decode(raw); err == nil {
		t.Fatal("missing required sources accepted")
	}
}

func TestGraphCostBudgetIsExplicitlySerial(t *testing.T) {
	s := fixture()
	cost := int64(100)
	s.Limits.MaxCostUnits = &cost
	p, e := Compile(s)
	if e != nil {
		t.Fatal(e)
	}
	if p.EffectiveParallelism != 1 || p.BudgetMode != "claude_estimated_cost_serial" {
		t.Fatalf("budget=%+v", p)
	}
	s.Operators[0].Execution.RuntimeProfile = "codex_exec"
	if _, e = Compile(s); e == nil {
		t.Fatal("unsupported cost runtime")
	}
}

func TestConditionsRequireTrustedVerifierAndCompleteThreeWayHandling(t *testing.T) {
	s := fixture()
	s.Edges[0].When = "PASS"
	if _, e := Compile(s); e == nil {
		t.Fatal("agent self-authorized conditional path")
	}
	s = fixture()
	s.Operators[0].Kind = "command"
	s.Operators[0].CommandProfile = "check"
	s.Operators[0].Execution = nil
	s.Operators[0].Outputs[0].Schema = "verdict-v1"
	s.Operators[1].Inputs[0].Schema = "verdict-v1"
	s.Operators[1].Inputs[1].Schema = "verdict-v1"
	s.Edges[0].When = "PASS"
	if _, e := Compile(s); e == nil {
		t.Fatal("incomplete UNKNOWN handling accepted")
	}
}

func TestLowerLegacy(t *testing.T) {
	s := fixture()
	ex := *s.Operators[0].Execution
	legacy := job.Spec{SchemaVersion: "v0.2", ProjectID: s.ProjectID, Mode: "single", Workspace: s.Workspace, Input: job.Input{Text: "你好"}, Execution: &ex, Limits: job.Limits{TimeoutSeconds: 60, MaxAttemptsPerTask: 1}}
	g, e := Lower(legacy)
	if e != nil {
		t.Fatal(e)
	}
	p, e := Compile(g)
	if e != nil || strings.Join(p.Order, ",") != "single" || p.Spec.Nodes[0].Instructions != "你好" {
		t.Fatalf("single: %v %+v", e, p)
	}
	legacy.Mode = "map_reduce"
	legacy.Execution = nil
	legacy.Map = &job.Map{Parallelism: 2, Partitions: []job.Partition{{Key: "a", Input: job.Input{Text: "甲"}, Execution: ex, ScopePaths: []string{"a"}}, {Key: "b", Input: job.Input{Text: "乙"}, Execution: ex, ScopePaths: []string{"b"}}}}
	legacy.Reduce = &job.Reduce{Strategy: "report_merge_v1", Input: job.Input{Text: "合并"}, Execution: ex}
	g, e = Lower(legacy)
	if e != nil {
		t.Fatal(e)
	}
	p, e = Compile(g)
	if e != nil || strings.Join(p.Order, ",") != "map_a,map_b,reduce" {
		t.Fatalf("map reduce: %v %+v", e, p)
	}
}
