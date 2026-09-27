package job

import (
	"reflect"
	"strings"
	"testing"
)

func TestExecutionEnvironmentDefaultsToProcess(t *testing.T) {
	e := testExecution()
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.EnvironmentName() != "process" {
		t.Fatalf("environment=%q", e.EnvironmentName())
	}
	s := Spec{ProjectID: "project", Workspace: Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("a", 40)}, Limits: Limits{TimeoutSeconds: 10}}
	task := e.Task(s, "task-key", "input")
	want := []string{"event_stream", "cancel", "job_io_v1", "environment:process"}
	if !reflect.DeepEqual(task.RequiredCapabilities, want) {
		t.Fatalf("required=%v want=%v", task.RequiredCapabilities, want)
	}
}

func TestExecutionEnvironmentRequirement(t *testing.T) {
	e := testExecution()
	e.Environment = "sandbox_fixture"
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	s := Spec{ProjectID: "project", Workspace: Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("a", 40)}, Limits: Limits{TimeoutSeconds: 10}}
	task := e.Task(s, "task-key", "input")
	found := false
	for _, capability := range task.RequiredCapabilities {
		found = found || capability == "environment:sandbox_fixture"
	}
	if !found {
		t.Fatalf("environment requirement missing: %v", task.RequiredCapabilities)
	}
	for _, bad := range []string{"Bad", "environment:qualified", "bad/name"} {
		e := testExecution()
		e.Environment = bad
		if err := e.Validate(); err == nil {
			t.Fatalf("invalid environment accepted: %q", bad)
		}
	}
}
