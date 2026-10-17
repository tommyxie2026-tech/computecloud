package worker

import (
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func TestGraphWorkerRejectsChangedContext(t *testing.T) {
	m, e := graph.CompileContext(graph.ContextRequest{Instructions: "你好", Model: "small", View: graph.EmptyView()})
	if e != nil {
		t.Fatal(e)
	}
	invocation := graph.Invocation{Version: "computecloud-job-io-v2", GoalID: "g", Generation: 1, NodeID: "n", Context: m, View: graph.EmptyView(), Outputs: []graph.Port{{Name: "result", Schema: "report-v1"}}}
	a := &pb.Assignment{Spec: &pb.TaskSpec{Input: &pb.Input{Text: m.Text}}, Job: &pb.JobExecution{GraphExecutionJson: job.JSON(invocation)}}
	if _, e := prepareGraph(a); e != nil {
		t.Fatal(e)
	}
	a.Spec.Input.Text += "changed"
	if _, e := prepareGraph(a); e == nil {
		t.Fatal("changed prompt accepted")
	}
}

func TestGraphOutputPreservesBusinessFailAndRejectsSelfVerification(t *testing.T) {
	view := graph.EmptyView()
	m, e := graph.CompileContext(graph.ContextRequest{Instructions: "verify", Model: "small", View: view})
	if e != nil {
		t.Fatal(e)
	}
	inv := graph.Invocation{Schemas: map[string]json.RawMessage{"verdict-v1": json.RawMessage(`{"enum":["PASS","FAIL","UNKNOWN"]}`)}, Version: "computecloud-job-io-v2", GoalID: "g", Generation: 1, NodeID: "n", Context: m, View: view, Outputs: []graph.Port{{Name: "result", Schema: "verdict-v1"}}}
	run := &jobRun{graph: &inv}
	files, e := finishGraphOutput(`{"version":"iu-node-output-v1","values":{"result":"FAIL"},"records":[]}`, nil, run)
	if e != nil || !strings.Contains(string(files["iu-output.json"]), "FAIL") {
		t.Fatalf("business fail: %v", e)
	}
	if _, e = finishGraphOutput(`{"version":"iu-node-output-v1","values":{"other":"PASS"},"records":[]}`, nil, run); e == nil {
		t.Fatal("undeclared output accepted")
	}
	if _, e = finishGraphOutput(`{"version":"iu-node-output-v1","values":{"result":"PASS"},"records":[{"id":"fact","kind":"result","payload":1,"validation":"verified","validity":"VALID"}]}`, nil, run); e == nil {
		t.Fatal("self verified memory accepted")
	}
	if _, e = finishGraphOutput(`{"version":"iu-node-output-v1","values":{"result":"PASS"},"records":[]}`, []byte("unhandled change"), run); e == nil {
		t.Fatal("workspace mutation accepted")
	}
}

func TestGraphOutputValidatesFrozenPortSchema(t *testing.T) {
	v := graph.EmptyView()
	m, e := graph.CompileContext(graph.ContextRequest{Instructions: "amount", Model: "small", View: v})
	if e != nil {
		t.Fatal(e)
	}
	inv := graph.Invocation{Version: "computecloud-job-io-v2", GoalID: "g", Generation: 1, NodeID: "n", Context: m, View: v, Outputs: []graph.Port{{Name: "result", Schema: "amount-v1"}}, Schemas: map[string]json.RawMessage{"amount-v1": json.RawMessage(`{"type":"integer"}`)}}
	if _, e = finishGraphOutput(`{"version":"iu-node-output-v1","values":{"result":"6000"},"records":[]}`, nil, &jobRun{graph: &inv}); e == nil {
		t.Fatal("string accepted as integer")
	}
	if _, e = finishGraphOutput(`{"version":"iu-node-output-v1","values":{"result":6000},"records":[]}`, nil, &jobRun{graph: &inv}); e != nil {
		t.Fatal(e)
	}
}
