package worker

import (
	"context"
	"reflect"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	envreg "github.com/tommyxie2026-tech/computecloud/internal/environment"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type environmentAdvertiseProvider struct{ recoveryRemoteProvider }

func (environmentAdvertiseProvider) Profile() string { return "environment_advertise_fixture" }
func (environmentAdvertiseProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{
		Runtime: []string{"remote_api"},
		Environment: []string{"process", "not_installed_environment_fixture"},
	}
}

func TestWorkerAdvertisesOnlyRegisteredCompatibleEnvironments(t *testing.T) {
	got := advertisedRuntimeCapabilities(environmentAdvertiseProvider{})
	for _, want := range []string{"runtime:remote_api", "environment:process"} {
		found := false
		for _, value := range got {
			found = found || value == want
		}
		if !found {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	for _, value := range got {
		if value == "environment:not_installed_environment_fixture" {
			t.Fatalf("unregistered Environment leaked into Worker advertisement: %v", got)
		}
	}
}

func TestRegisteredFixtureEnvironmentCanBeAdvertisedWithoutSchedulerChange(t *testing.T) {
	name := "worker_fixture_environment"
	if _, ok := envreg.Lookup(name); !ok {
		if err := envreg.Register(envreg.Descriptor{
			Name: name, Version: "1",
			IsolationClass: "container", FilesystemMode: "isolated", NetworkMode: "restricted",
		}); err != nil {
			t.Fatal(err)
		}
	}
	caps := adapter.CapabilitySet{Environment: []string{name}}
	caps.Environment = envreg.InstalledCompatible(caps.Environment)
	if !reflect.DeepEqual(caps.Advertised(), []string{"environment:" + name}) {
		t.Fatalf("fixture Environment advertisement=%v", caps.Advertised())
	}
}

type deniedEnvironmentProvider struct{ recoveryRemoteProvider }

func (deniedEnvironmentProvider) Profile() string { return "denied_environment_fixture" }
func (deniedEnvironmentProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{Runtime: []string{"remote_api"}, Environment: []string{"fixture_container_environment"}}
}

func TestWorkerRejectsDeniedEnvironmentBeforeWorkspaceOrRuntimeStart(t *testing.T) {
	if _, ok := envreg.Lookup("fixture_container_environment"); !ok {
		if err := envreg.Register(envreg.Descriptor{
			Name: "fixture_container_environment", Version: "1",
			IsolationClass: "container", FilesystemMode: "isolated", NetworkMode: "restricted",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := adapter.Lookup("denied_environment_fixture"); !ok {
		if err := adapter.Register(deniedEnvironmentProvider{}); err != nil {
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
				"denied_environment_fixture": {Version: "v1", Models: []string{"model"}, Credentials: []string{"cred"}},
			},
			Policies: map[string]config.Policy{"policy": {}},
			Verifiers: map[string][][]string{"accept": {}},
		},
	}
	a := &pb.Assignment{
		TaskId: "task", AttemptId: "attempt", Generation: 1, LeaseToken: "lease",
		Spec: &pb.TaskSpec{
			RuntimeProfile: "denied_environment_fixture", Model: "model", CredentialRef: "cred",
			PolicyRef: "policy", AcceptanceProfile: "accept",
			Workspace: &pb.Workspace{RepositoryRef: "repo", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Input: &pb.Input{Text: "fixture"},
			RequiredCapabilities: []string{"environment:fixture_container_environment"},
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
	if done.ErrorCode != "ENVIRONMENT_POLICY_DENIED" || done.Success {
		t.Fatalf("completion=%+v", done)
	}
	if len(runtimeRef) != 0 {
		t.Fatalf("Runtime started before Environment policy denial: %q", runtimeRef)
	}
}
