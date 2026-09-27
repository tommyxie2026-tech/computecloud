package worker

import (
	"context"
	"reflect"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	toolreg "github.com/tommyxie2026-tech/computecloud/internal/tool"
)

type toolAdvertiseProvider struct{ recoveryRemoteProvider }

func (toolAdvertiseProvider) Profile() string { return "tool_advertise_fixture" }
func (toolAdvertiseProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{
		Runtime: []string{"remote_api"},
		Tools: []string{"job_io_v1", "not_installed_fixture"},
		Environment: []string{"process"},
	}
}

func TestWorkerAdvertisesOnlyInstalledCompatibleTools(t *testing.T) {
	got := advertisedRuntimeCapabilities(toolAdvertiseProvider{})
	for _, want := range []string{"runtime:remote_api", "tool:job_io_v1", "environment:process"} {
		found := false
		for _, value := range got {
			found = found || value == want
		}
		if !found {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	for _, value := range got {
		if value == "tool:not_installed_fixture" {
			t.Fatalf("unregistered Tool leaked into Worker advertisement: %v", got)
		}
	}
}

func TestRegisteredFixtureToolCanBeAdvertisedWithoutSchedulerChange(t *testing.T) {
	name := "worker_fixture_tool"
	if _, ok := toolreg.Lookup(name); !ok {
		if err := toolreg.Register(toolreg.Descriptor{Name: name, Version: "1", SideEffect: toolreg.ReadOnly}); err != nil {
			t.Fatal(err)
		}
	}
	caps := adapter.CapabilitySet{Tools: []string{name}}
	caps.Tools = toolreg.InstalledCompatible(caps.Tools)
	if !reflect.DeepEqual(caps.Advertised(), []string{"tool:" + name}) {
		t.Fatalf("fixture Tool advertisement=%v", caps.Advertised())
	}
}


type deniedToolProvider struct{ recoveryRemoteProvider }

func (deniedToolProvider) Profile() string { return "denied_tool_fixture" }
func (deniedToolProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{Runtime: []string{"remote_api"}, Tools: []string{"fixture_mutating_tool"}}
}

func TestWorkerRejectsDeniedToolBeforeRuntimeStart(t *testing.T) {
	if _, ok := toolreg.Lookup("fixture_mutating_tool"); !ok {
		if err := toolreg.Register(toolreg.Descriptor{Name: "fixture_mutating_tool", Version: "1", SideEffect: toolreg.Mutating}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := adapter.Lookup("denied_tool_fixture"); !ok {
		if err := adapter.Register(deniedToolProvider{}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(t.TempDir(), store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := &Worker{
		db: db,
		cfg: config.Worker{
			Runtimes: map[string]config.Runtime{
				"denied_tool_fixture": {Version: "v1", Models: []string{"model"}, Credentials: []string{"cred"}},
			},
			Policies: map[string]config.Policy{"policy": {}},
			Verifiers: map[string][][]string{"accept": {}},
		},
	}
	a := &pb.Assignment{
		TaskId: "task", AttemptId: "attempt", Generation: 1, LeaseToken: "lease",
		Spec: &pb.TaskSpec{
			RuntimeProfile: "denied_tool_fixture", Model: "model", CredentialRef: "cred",
			PolicyRef: "policy", AcceptanceProfile: "accept",
			Workspace: &pb.Workspace{RepositoryRef: "repo", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Input: &pb.Input{Text: "fixture"},
			RequiredCapabilities: []string{"tool:fixture_mutating_tool"},
		},
	}
	if _, err = db.SQL.Exec("INSERT INTO runs(id,assignment,state) VALUES(?,?,?)", a.AttemptId, enc(a), "ACCEPTED"); err != nil {
		t.Fatal(err)
	}
	w.execute(context.Background(), a)
	var raw []byte
	var runtimeRef []byte
	if err = db.SQL.QueryRow("SELECT completion,runtime_ref FROM runs WHERE id=?", a.AttemptId).Scan(&raw, &runtimeRef); err != nil {
		t.Fatal(err)
	}
	done := new(pb.CompleteRequest)
	if err = dec(raw, done); err != nil {
		t.Fatal(err)
	}
	if done.ErrorCode != "TOOL_POLICY_DENIED" || done.Success {
		t.Fatalf("completion=%+v", done)
	}
	if len(runtimeRef) != 0 {
		t.Fatalf("Runtime started before Tool policy denial: %q", runtimeRef)
	}
}
