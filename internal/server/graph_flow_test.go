package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/iuexperiment"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestGraphTwoWorkerMemoryJoinAndTrustedCommandFlow(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		rule, applied, amount int
		verdict               string
	}{
		{"discount_1000", 1000, 1000, 11000, "PASS"},
		{"revised_discount_2000", 2000, 2000, 10000, "PASS"},
		{"stale_rule_rejected", 2000, 1000, 11000, "FAIL"},
		{"missing_global_rule_rejected", 1000, 0, 12000, "FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) { runGraphOrdersSimulation(t, tc.rule, tc.applied, tc.amount, tc.verdict) })
	}
}
func runGraphOrdersSimulation(t *testing.T, ruleDiscount, appliedDiscount, wantAmount int, wantVerdict string, incremental ...bool) {
	runGraphOrdersVariant(t, ruleDiscount, appliedDiscount, wantAmount, wantVerdict, len(incremental) > 0 && incremental[0], "rule_change")
}
func runGraphOrdersVariant(t *testing.T, ruleDiscount, appliedDiscount, wantAmount int, wantVerdict string, incrementalMode bool, variant string) {
	var replay graph.ReplayBinding
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
		if incrementalMode {
			r := c.Runtimes["codex_exec"]
			replay = graph.ReplayBinding{Version: "1", RuntimeProfile: "codex_exec", RuntimeVersion: r.Version, Model: "model-c", EnvironmentHash: graph.Hash([]byte("CI fixture tools v1")), RuntimeConfigHash: graph.Hash(job.JSON(r)), PolicyHash: graph.Hash(job.JSON(c.Policies["review"])), ReadOnly: true}
			c.GraphReplayBindings = map[string]graph.ReplayBinding{"fixture": replay}
		}
		c.Runtimes["command_exec"] = config.Runtime{Executable: verifier, Version: "iu-orders-verifier-1", Models: []string{"verify"}, Credentials: []string{"account"}}
	}, func(c *config.Server) {
		c.Jobs.GraphsEnabled = true
		c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:evaluate")
		if incrementalMode {
			c.Users[0].Scopes = append(c.Users[0].Scopes, "goals:import", "goals:invalidate", "goals:propose")
			c.Jobs.GraphReplayBindings = map[string]graph.ReplayBinding{"fixture": replay}
		}
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
	if incrementalMode && variant == "duplicate" {
		b.Text = a.Text
	}
	if incrementalMode && variant == "different_customer" {
		b.Text = `{"order_id":"b","customer_id":"d","paid_cents":6000}`
	}
	b.SHA256 = graph.Hash([]byte(b.Text))
	rule := graph.Source{ID: "discount", Text: fmt.Sprintf(`{"threshold_cents":10000,"discount_cents":%d}`, ruleDiscount)}
	rule.SHA256 = graph.Hash([]byte(rule.Text))
	s.Sources = []graph.Source{a, b, rule}
	s.Rules = []graph.Span{{Source: rule.ID, Start: 0, End: len(rule.Text), SHA256: rule.SHA256}}
	s.Operators = []graph.Operator{
		{ID: "extract", Version: "1", Kind: "agent", Execution: &ex, Outputs: []graph.Port{{Name: "result", Schema: "amount-v1"}}},
		{ID: "join", Version: "1", Kind: "join", Inputs: []graph.Port{{Name: "a", Schema: "amount-v1"}, {Name: "b", Schema: "amount-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "amounts-v1"}}},
		{ID: "discount", Version: "1", Kind: "agent", Execution: &ex, Inputs: []graph.Port{{Name: "amounts", Schema: "amounts-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "amount-v1"}}},
		{ID: "verify", Version: "1", Kind: "command", CommandProfile: "verify", Inputs: []graph.Port{{Name: "amount", Schema: "amount-v1"}}, Outputs: []graph.Port{{Name: "result", Schema: "verdict-v1"}, {Name: "expected_total", Schema: "optional-amount-v1"}}},
	}
	s.Nodes = []graph.Node{{ID: "a", Operator: "extract", Instructions: "fixture-amount:6000 fixture-record:order_a", Spans: []graph.Span{{Source: a.ID, Start: 0, End: len(a.Text), SHA256: a.SHA256}}}, {ID: "b", Operator: "extract", Instructions: "fixture-amount:6000 fixture-record:order_b", Spans: []graph.Span{{Source: b.ID, Start: 0, End: len(b.Text), SHA256: b.SHA256}}}, {ID: "join", Operator: "join", JoinPolicy: "all_success"}, {ID: "discount", Operator: "discount", Instructions: fmt.Sprintf("fixture-discount:%d fixture-record:total", appliedDiscount)}, {ID: "verify", Operator: "verify", Parameters: json.RawMessage(`{}`), Spans: []graph.Span{{Source: a.ID, Start: 0, End: len(a.Text), SHA256: a.SHA256}, {Source: b.ID, Start: 0, End: len(b.Text), SHA256: b.SHA256}}}}
	s.Edges = []graph.Edge{{FromNode: "a", FromPort: "result", ToNode: "join", ToPort: "a", Type: "memory"}, {FromNode: "b", FromPort: "result", ToNode: "join", ToPort: "b", Type: "memory"}, {FromNode: "join", FromPort: "result", ToNode: "discount", ToPort: "amounts", Type: "memory"}, {FromNode: "discount", FromPort: "result", ToNode: "verify", ToPort: "amount", Type: "memory"}}
	s.Outputs = []graph.Output{{Node: "discount", Port: "result"}, {Node: "verify", Port: "result"}}
	if incrementalMode {
		s.Schemas["order-v1"] = json.RawMessage(`{"type":"object","required":["order_id","customer_id","paid_cents"],"additionalProperties":false,"properties":{"order_id":{"type":"string"},"customer_id":{"type":"string"},"paid_cents":{"type":"integer"}}}`)
		s.Schemas["orders-v1"] = json.RawMessage(`{"type":"object","additionalProperties":{"type":"object"}}`)
		s.Operators[0].Outputs[0].Schema = "order-v1"
		for i := range s.Operators[1].Inputs {
			s.Operators[1].Inputs[i].Schema = "order-v1"
		}
		s.Operators[1].Outputs[0].Schema = "orders-v1"
		s.Operators[2].Inputs[0].Schema = "orders-v1"
		s.Nodes[0].Instructions = "fixture-order-object fixture-record:order_a"
		s.Nodes[1].Instructions = "fixture-order-object fixture-record:order_b"
		s.Nodes[3].Instructions = fmt.Sprintf("fixture-customer-total:%d fixture-record:total", appliedDiscount)
		s.Version = graph.ReuseVersion
		s.Limits.MaxReplans = 1
		for i := range s.Operators {
			if s.Operators[i].Execution != nil {
				s.Operators[i].Execution.ReplaySafe = true
			}
		}
		for i := range s.Nodes {
			n := &s.Nodes[i]
			if n.ID == "a" || n.ID == "b" {
				n.ReplayProfile = "fixture"
			}
			if n.ID == "discount" || n.ID == "verify" {
				n.Rules = append([]graph.Span{}, s.Rules...)
			}
		}
		s.Rules = nil
	}
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
	if string(result.Outputs["discount/result"]) != strconv.Itoa(wantAmount) || string(result.Outputs["verify/result"]) != strconv.Quote(wantVerdict) {
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
	if incrementalMode {
		runOrdersIncrementalComparison(t, h, j, s, st, variant)
		return
	}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, GraphEvaluation{ID: "orders-pass", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: wantVerdict, EvidenceHash: graph.Hash(j.result)}); e != nil {
		t.Fatal(e)
	}
	st, e = h.s.GraphStatus(h.ctx, j.ID)
	wantGoal := "SUCCEEDED"
	if wantVerdict == "FAIL" {
		wantGoal = "FAILED"
	}
	if e != nil || st.GoalState != wantGoal {
		t.Fatalf("business verdict not respected: %+v %v", st, e)
	}
	var workers int
	if e = h.s.db.SQL.QueryRow(`SELECT count(DISTINCT a.worker) FROM attempts a JOIN tasks t ON t.id=a.task JOIN iu_nodes n ON n.task_id=t.id WHERE t.job_id=? AND n.node_id IN ('a','b')`, j.ID).Scan(&workers); e != nil || workers != 2 {
		t.Fatalf("root execution did not use two Workers: %d %v", workers, e)
	}
	t.Logf("orders proof: rule=%d applied=%d amount=%d verdict=%s workers=%d memory_records=%d goal=%s", ruleDiscount, appliedDiscount, wantAmount, wantVerdict, workers, len(view.Records), st.GoalState)

}

// Local fixture processes exercise real Server/Worker Task/Attempt and admission.
func TestGraphIncrementalOrdersMatchesFullRecompute(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  int
	}{{"rule_change", 11000}, {"duplicate", 6000}, {"different_customer", 12000}, {"two_hop_counterevidence", 11000}} {
		t.Run(tc.name, func(t *testing.T) { runGraphOrdersVariant(t, 1000, 1000, tc.old, "PASS", true, tc.name) })
	}
}
func runOrdersIncrementalComparison(t *testing.T, h *jobHarness, prior *Job, spec graph.Spec, st *GraphStatus, variant string) {
	t.Helper()
	rule := graph.Source{ID: "discount", Text: `{"threshold_cents":10000,"discount_cents":1000}`}
	rule.SHA256 = graph.Hash([]byte(rule.Text))
	if e := h.s.InvalidateGraph(h.ctx, prior.ID, graph.InvalidationEvent{ID: "rule-changed", GoalID: st.GoalID, Generation: 1, Reason: "RULE_CHANGED", EvidenceHash: graph.Hash([]byte("discount=2000")), Sources: []graph.SourceRef{{ID: rule.ID, SHA256: rule.SHA256}}}); e != nil {
		t.Fatal(e)
	}
	if e := h.s.reconcileGraphInvalidations(h.ctx); e != nil {
		t.Fatal(e)
	}
	ev := GraphEvaluation{ID: "rule-change-fail", OutputHash: st.OutputHash, ExpectedValidityRevision: 1, Verifier: "external", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: graph.Hash([]byte("new rule invalidates old business result"))}
	if e := h.s.EvaluateGraph(h.ctx, prior.ID, ev); e != nil {
		t.Fatal(e)
	}
	rule.Text = `{"threshold_cents":10000,"discount_cents":2000}`
	rule.SHA256 = graph.Hash([]byte(rule.Text))
	for i := range spec.Sources {
		if spec.Sources[i].ID == rule.ID {
			spec.Sources[i] = rule
		}
	}
	for i := range spec.Nodes {
		n := &spec.Nodes[i]
		if n.ID == "discount" {
			n.Instructions = "fixture-customer-total:2000 fixture-record:total"
		}
		for k := range n.Rules {
			if n.Rules[k].Source == rule.ID {
				n.Rules[k] = graph.Span{Source: rule.ID, Start: 0, End: len(rule.Text), SHA256: rule.SHA256}
			}
		}
	}
	spec.Reuse = []graph.ReuseBinding{{GrantID: "reuse_a", NodeID: "a"}, {GrantID: "reuse_b", NodeID: "b"}}
	plan, e := graph.Compile(spec)
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range []string{"a", "b"} {
		view, e := h.s.GraphMemory(h.ctx, prior.ID, node)
		if e != nil {
			t.Fatal(e)
		}
		ids := []string{}
		for _, r := range view.Records {
			ids = append(ids, r.ID)
		}
		in := GraphImportRequest{ID: "reuse_" + node, Purpose: "node_result", NodeID: node, ViewHash: view.SHA256, RecordIDs: ids, ExpectedRevision: 1, TargetGeneration: 2, TargetPlanHash: plan.Digest, ExpiresAt: store.Now() + 30000}
		if _, e = h.s.IssueGraphImport(h.ctx, prior.ID, in); e != nil {
			t.Fatal(e)
		}
	}
	current, e := h.s.GraphStatus(h.ctx, prior.ID)
	if e != nil {
		t.Fatal(e)
	}
	in := replanProposal(st.GoalID, current.EvaluationID, h.spec("single"))
	in.ProposedPlan.JobSpecHash = plan.Digest
	in.Evidence = goal.ReplanEvidence{FailureClass: goal.FailureTest, Evidence: []goal.Evidence{{Type: "iu_graph_evaluation", Fact: graphEvaluationFact(ev)}}}
	next, d, e := h.s.PublishGoalReplan(h.ctx, prior.ID, in, "", job.JSON(spec))
	if e != nil || !d.Allowed {
		t.Fatalf("publish %+v %v", d, e)
	}
	next = h.wait(t, next.ID)
	if next.State != "SUCCEEDED" {
		t.Fatalf("incremental failed %+v", next)
	}
	spec.Reuse = nil
	spec.Limits.MaxReplans = 0
	full, e := h.s.SubmitGraph(h.ctx, "full-comparison", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	full = h.wait(t, full.ID)
	if full.State != "SUCCEEDED" {
		t.Fatalf("full failed %+v", full)
	}
	a, e := h.s.GraphStatus(h.ctx, next.ID)
	if e != nil {
		t.Fatal(e)
	}
	b, e := h.s.GraphStatus(h.ctx, full.ID)
	if e != nil {
		t.Fatal(e)
	}
	if a.OutputHash != b.OutputHash {
		t.Fatalf("incremental/full output differs %s %s", next.result, full.result)
	}
	var result struct {
		Outputs map[string]json.RawMessage `json:"outputs"`
	}
	json.Unmarshal(next.result, &result)
	wantNew := "10000"
	if variant == "duplicate" {
		wantNew = "6000"
	}
	if variant == "different_customer" {
		wantNew = "12000"
	}
	if string(result.Outputs["discount/result"]) != wantNew || string(result.Outputs["verify/result"]) != `"PASS"` {
		t.Fatal(string(next.result))
	}
	var newTasks, fullTasks, newAttempts, fullAttempts int
	for _, q := range []struct {
		id              string
		tasks, attempts *int
	}{{next.ID, &newTasks, &newAttempts}, {full.ID, &fullTasks, &fullAttempts}} {
		if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, q.id).Scan(q.tasks); e != nil {
			t.Fatal(e)
		}
		if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=?`, q.id).Scan(q.attempts); e != nil {
			t.Fatal(e)
		}
	}
	if newTasks != 2 || newAttempts != 2 || fullTasks != 4 || fullAttempts != 4 {
		t.Fatalf("incremental tasks/attempts %d/%d full %d/%d", newTasks, newAttempts, fullTasks, fullAttempts)
	}
	reused := 0
	for _, n := range a.Nodes {
		if n.ExecutionKind == "REUSED" {
			reused++
		}
	}
	if reused != 2 || a.GoalState != "EVALUATING" {
		t.Fatalf("reuse=%d goal=%s", reused, a.GoalState)
	}
	if variant == "two_hop_counterevidence" {
		root, e := h.s.GraphMemory(h.ctx, next.ID, "a")
		if e != nil || len(root.Records) != 1 {
			t.Fatalf("root %+v %v", root, e)
		}
		event := graph.InvalidationEvent{ID: "counterevidence", GoalID: a.GoalID, Generation: 2, Reason: "SOURCE_REVISED", EvidenceHash: graph.Hash([]byte("counterevidence")), Records: []string{root.Records[0].ID}}
		if e = h.s.InvalidateGraph(h.ctx, next.ID, event); e != nil {
			t.Fatal(e)
		}
		if e = h.s.reconcileGraphInvalidations(h.ctx); e != nil {
			t.Fatal(e)
		}
		view, e := h.s.GraphMemory(h.ctx, next.ID, "verify")
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, r := range view.Records {
			if r.ID == "total" {
				found = true
				if r.Validity != "INVALID" {
					t.Fatalf("two-hop derived total survived %+v", r)
				}
			}
		}
		if !found {
			t.Fatal("missing derived total")
		}
		if e = h.s.EvaluateGraph(h.ctx, next.ID, GraphEvaluation{ID: "stale-pass", ExpectedValidityRevision: 1, OutputHash: a.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: a.OutputHash}); e == nil {
			t.Fatal("counterevidence accepted stale PASS")
		}
	} else if e = h.s.EvaluateGraph(h.ctx, next.ID, GraphEvaluation{ID: "current-pass", OutputHash: a.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: a.OutputHash}); e != nil {
		t.Fatal(e)
	}
	sources := []string{}
	for _, n := range a.Nodes {
		if n.Reuse != nil && n.Reuse.Decision == "REUSE" {
			sources = append(sources, n.Reuse.SourceJob+"/"+n.Reuse.SourceNode)
		}
	}
	dataset := graph.Hash(job.JSON(b.Plan.Spec.Sources))
	incrementalDataset := graph.Hash(job.JSON(a.Plan.Spec.Sources))
	zero := 0
	fullCalls, incCalls := observedFixtureAgentCount(t, h, full.ID), observedFixtureAgentCount(t, h, next.ID)
	comparison, e := iuexperiment.CompareIncremental(
		iuexperiment.IncrementalArm{DatasetHash: dataset, OutputHash: b.OutputHash, BusinessVerdict: "PASS", Tasks: fullTasks, Attempts: fullAttempts, FixtureAgentInvocations: &fullCalls, ProviderCalls: &zero},
		iuexperiment.IncrementalArm{DatasetHash: incrementalDataset, OutputHash: a.OutputHash, BusinessVerdict: "PASS", Tasks: newTasks, Attempts: newAttempts, ReusedNodes: reused, FixtureAgentInvocations: &incCalls, ProviderCalls: &zero, Provenance: sources})
	if e != nil || !comparison.Equivalent || !comparison.FewerTasks {
		t.Fatal("incomplete comparison", e)
	}
	t.Log("IU_INCREMENTAL_EVIDENCE " + string(job.JSON(comparison)))
	t.Logf("fixture comparison: full_tasks=%d full_attempts=%d incremental_tasks=%d incremental_attempts=%d reused=%d actual_provider_calls=0 usage=unknown", fullTasks, fullAttempts, newTasks, newAttempts, reused)
}

func observedFixtureAgentCount(t *testing.T, h *jobHarness, id string) int {
	t.Helper()
	rows, e := h.s.db.SQL.Query(`SELECT t.spec FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=?`, id)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var raw []byte
		var spec pb.TaskSpec
		if e = rows.Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e = decode(raw, &spec); e != nil {
			t.Fatal(e)
		}
		if spec.RuntimeProfile != "command_exec" {
			count++
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return count
}
