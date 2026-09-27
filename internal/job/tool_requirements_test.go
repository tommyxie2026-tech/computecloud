package job

import (
	"reflect"
	"strings"
	"testing"
)

func testExecution() Execution {
	return Execution{
		Engine: "codex", RuntimeProfile: "codex_exec", Model: "fixture-model",
		CredentialRef: "cred", PolicyRef: "policy", AcceptanceProfile: "accept",
	}
}

func TestExecutionToolsValidationAndTaskRequirements(t *testing.T) {
	e := testExecution()
	e.Tools = []string{"job_io_v1", "artifact_inputs_v1"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	s := Spec{ProjectID: "project", Workspace: Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("a", 40)}, Limits: Limits{TimeoutSeconds: 10}}
	task := e.Task(s, "task-key", "input")
	want := []string{"event_stream", "cancel", "job_io_v1", "tool:artifact_inputs_v1", "tool:job_io_v1", "environment:process"}
	if !reflect.DeepEqual(task.RequiredCapabilities, want) {
		t.Fatalf("required=%v want=%v", task.RequiredCapabilities, want)
	}
}

func TestExecutionToolsRejectInvalidOrDuplicate(t *testing.T) {
	for _, tools := range [][]string{
		{"Bad"},
		{"tool:qualified"},
		{"job_io_v1", "job_io_v1"},
	} {
		e := testExecution()
		e.Tools = tools
		if err := e.Validate(); err == nil {
			t.Fatalf("invalid tools accepted: %v", tools)
		}
	}
	e := testExecution()
	for i := 0; i < 17; i++ {
		e.Tools = append(e.Tools, "tool_"+string(rune('a'+i)))
	}
	if err := e.Validate(); err == nil {
		t.Fatal("too many tools accepted")
	}
}
