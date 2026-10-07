package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	envreg "github.com/tommyxie2026-tech/computecloud/internal/environment"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

const fixtureEnvironmentName = "fixture_isolated_env"

type fixtureEnvironmentProvider struct{}

func (fixtureEnvironmentProvider) Descriptor() envreg.Descriptor {
	return envreg.Descriptor{
		Name: fixtureEnvironmentName, Version: "fixture-v1",
		IsolationClass: "remote", FilesystemMode: "remote", NetworkMode: "restricted",
	}
}
func (fixtureEnvironmentProvider) Prepare(_ context.Context, req envreg.PrepareRequest, record func(envreg.Ref) error) (envreg.Prepared, error) {
	if req.AttemptID == "" || req.CWD == "" {
		return envreg.Prepared{}, errors.New("fixture environment missing identity/workspace")
	}
	ref := envreg.Ref{Provider: fixtureEnvironmentName, ID: req.AttemptID}
	if record != nil {
		if err := record(ref); err != nil {
			return envreg.Prepared{}, err
		}
	}
	return envreg.Prepared{
		Ref: ref,
		CWD: req.CWD,
		Env: append(append([]string(nil), req.Env...), "COMPUTECLOUD_ENV_FIXTURE=1"),
	}, nil
}
func (fixtureEnvironmentProvider) Activate(context.Context, envreg.Prepared) error { return nil }
func (fixtureEnvironmentProvider) Inspect(_ context.Context, ref envreg.Ref) (envreg.Inspection, error) {
	if ref.Provider != fixtureEnvironmentName {
		return envreg.Inspection{State: envreg.StateUnknown, Cleanup: envreg.CleanupUnknown}, errors.New("foreign environment ref")
	}
	if strings.Contains(ref.ID, "unknown-env") {
		return envreg.Inspection{State: envreg.StateUnknown, Cleanup: envreg.CleanupUnknown}, nil
	}
	return envreg.Inspection{State: envreg.StateActive, Cleanup: envreg.CleanupPending}, nil
}
func (fixtureEnvironmentProvider) Release(_ context.Context, ref envreg.Ref) (envreg.ReleaseResult, error) {
	if ref.Provider != fixtureEnvironmentName {
		return envreg.ReleaseResult{State: envreg.StateUnknown, Cleanup: envreg.CleanupUnknown}, errors.New("foreign environment ref")
	}
	if strings.Contains(ref.ID, "unknown-env") {
		return envreg.ReleaseResult{State: envreg.StateUnknown, Cleanup: envreg.CleanupUnknown}, nil
	}
	return envreg.ReleaseResult{State: envreg.StateReleased, Cleanup: envreg.CleanupConfirmed}, nil
}

type environmentRemoteParser struct{}

func (environmentRemoteParser) Line([]byte) error { return nil }
func (environmentRemoteParser) Outcome() adapter.Outcome {
	return adapter.Outcome{Final: true, Success: true, Result: "environment fixture ok"}
}

type environmentRemoteProvider struct{}

func (environmentRemoteProvider) Profile() string                                    { return "environment_remote_fixture" }
func (environmentRemoteProvider) Version(r config.Runtime) string                    { return r.Version }
func (environmentRemoteProvider) Transport() string                                  { return "remote_api" }
func (environmentRemoteProvider) Probe(context.Context, config.Runtime) error        { return nil }
func (environmentRemoteProvider) Args(*pb.TaskSpec, config.Policy) ([]string, error) { return nil, nil }
func (environmentRemoteProvider) Parser(func(string, []byte) error) adapter.StreamParser {
	return environmentRemoteParser{}
}
func (environmentRemoteProvider) Prepare(req adapter.PrepareRequest) (adapter.PreparedExecution, error) {
	found := false
	for _, item := range req.Env {
		if item == "COMPUTECLOUD_ENV_FIXTURE=1" {
			found = true
			break
		}
	}
	if !found {
		return adapter.PreparedExecution{}, errors.New("environment-prepared env missing")
	}
	return adapter.PreparedExecution{
		Profile:   "environment_remote_fixture",
		Runtime:   req.Runtime,
		Env:       append([]string(nil), req.Env...),
		CWD:       req.CWD,
		Input:     req.Input,
		Emit:      req.Emit,
		Stderr:    req.Stderr,
		StopGrace: req.StopGrace,
	}, nil
}
func (environmentRemoteProvider) Start(_ context.Context, _ adapter.PreparedExecution, started func(adapter.ExecutionRef) error) adapter.StartResult {
	ref := adapter.ExecutionRef{Provider: "environment_remote_fixture", Transport: "remote_api", ID: "env-runtime"}
	if started != nil {
		if err := started(ref); err != nil {
			return adapter.StartResult{Ref: ref, State: adapter.RuntimeUnknown, Cleanup: adapter.CleanupUnknown, Err: err}
		}
	}
	return adapter.StartResult{
		Ref: ref, State: adapter.RuntimeExited, Cleanup: adapter.CleanupConfirmed,
		Outcome: adapter.Outcome{Final: true, Success: true, Result: "environment fixture ok"},
	}
}
func (environmentRemoteProvider) Inspect(context.Context, config.Runtime, adapter.ExecutionRef) (adapter.Inspection, error) {
	return adapter.Inspection{State: adapter.RuntimeExited, Cleanup: adapter.CleanupConfirmed}, nil
}
func (environmentRemoteProvider) Stop(context.Context, config.Runtime, adapter.ExecutionRef, time.Duration) (adapter.StopResult, error) {
	return adapter.StopResult{State: adapter.RuntimeExited, Cleanup: adapter.CleanupConfirmed}, nil
}
func (environmentRemoteProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{
		Runtime:     []string{"remote_api"},
		Environment: []string{fixtureEnvironmentName},
	}
}
func (environmentRemoteProvider) SupportsGateway() bool { return false }

type environmentLocalMisclaimProvider struct{ environmentRemoteProvider }

func (environmentLocalMisclaimProvider) Profile() string   { return "environment_local_misclaim_fixture" }
func (environmentLocalMisclaimProvider) Transport() string { return "local_cli" }
func (environmentLocalMisclaimProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{Runtime: []string{"local_cli"}, Environment: []string{fixtureEnvironmentName}}
}

var registerEnvironmentFixtures sync.Once

func ensureEnvironmentExecutionFixtures(t *testing.T) {
	t.Helper()
	var registerErr error
	registerEnvironmentFixtures.Do(func() {
		if _, ok := envreg.LookupProvider(fixtureEnvironmentName); !ok {
			registerErr = envreg.RegisterProvider(fixtureEnvironmentProvider{})
			if registerErr != nil {
				return
			}
		}
		if _, ok := adapter.Lookup("environment_remote_fixture"); !ok {
			registerErr = adapter.Register(environmentRemoteProvider{})
		}
	})
	if registerErr != nil {
		t.Fatal(registerErr)
	}
}

func environmentAssignment(t *testing.T, attempt string, cleanupUnknown bool) (*Worker, *pb.Assignment, func()) {
	t.Helper()
	ensureEnvironmentExecutionFixtures(t)
	dir := t.TempDir()
	db, err := store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	repo, commit := workspaceTestRepo(t)
	if cleanupUnknown {
		attempt = "unknown-env-" + attempt
	}
	w := &Worker{
		db: db,
		cfg: config.Worker{
			DataDir:              dir,
			StopGraceMS:          50,
			WorkspaceRetentionMS: 1000,
			Runtimes: map[string]config.Runtime{
				"environment_remote_fixture": {
					Version:     "fixture-v1",
					Models:      []string{"fixture-model"},
					Credentials: []string{"fixture-credential"},
				},
			},
			Repositories: map[string]string{"repo": repo},
			Policies: map[string]config.Policy{
				"policy": {AllowedEnvironments: []string{fixtureEnvironmentName}},
			},
			Verifiers: map[string][][]string{"accept": {}},
		},
	}
	a := &pb.Assignment{
		TaskId:     "task-" + attempt,
		AttemptId:  attempt,
		Generation: 1,
		LeaseToken: "token",
		Spec: &pb.TaskSpec{
			RuntimeProfile:       "environment_remote_fixture",
			Model:                "fixture-model",
			CredentialRef:        "fixture-credential",
			PolicyRef:            "policy",
			AcceptanceProfile:    "accept",
			RequiredCapabilities: []string{"environment:" + fixtureEnvironmentName},
			Workspace:            &pb.Workspace{RepositoryRef: "repo", BaseCommit: commit},
			Input:                &pb.Input{Text: "environment fixture"},
		},
	}
	if _, err = db.SQL.Exec("INSERT INTO runs(id,assignment,state) VALUES(?,?,?)", a.AttemptId, enc(a), "ACCEPTED"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return w, a, func() { db.Close() }
}

func TestEnvironmentProviderWrapsRuntimeExecution(t *testing.T) {
	w, a, closeFn := environmentAssignment(t, "env-attempt", false)
	defer closeFn()

	w.execute(context.Background(), a)

	var completion []byte
	var provider, state, cleanup string
	var refBytes []byte
	if err := w.db.SQL.QueryRow(`SELECT completion,environment_provider,environment_ref,environment_state,environment_cleanup
		FROM runs WHERE id=?`, a.AttemptId).Scan(&completion, &provider, &refBytes, &state, &cleanup); err != nil {
		t.Fatal(err)
	}
	done := new(pb.CompleteRequest)
	if err := dec(completion, done); err != nil {
		t.Fatal(err)
	}
	if !done.Success || !done.CleanupConfirmed {
		t.Fatalf("completion=%+v", done)
	}
	ref, err := envreg.DecodeRef(refBytes)
	if err != nil {
		t.Fatal(err)
	}
	if provider != fixtureEnvironmentName || ref.Provider != fixtureEnvironmentName || ref.ID != a.AttemptId {
		t.Fatalf("provider=%s ref=%+v", provider, ref)
	}
	if state != string(envreg.StateReleased) || cleanup != string(envreg.CleanupConfirmed) {
		t.Fatalf("environment state=%s cleanup=%s", state, cleanup)
	}
}

func TestLocalCLIRejectsClaimedRemoteEnvironmentBeforeStart(t *testing.T) {
	ensureEnvironmentExecutionFixtures(t)
	provider := environmentLocalMisclaimProvider{}
	if _, ok := adapter.Lookup(provider.Profile()); !ok {
		if err := adapter.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	for _, capability := range advertisedRuntimeCapabilities(provider) {
		if capability == "environment:"+fixtureEnvironmentName {
			t.Fatal("host CLI advertised remote isolation")
		}
	}
	w, a, closeFn := environmentAssignment(t, "local-misclaim", false)
	defer closeFn()
	a.Spec.RuntimeProfile = provider.Profile()
	w.cfg.Runtimes[provider.Profile()] = config.Runtime{Version: "fixture-v1", Models: []string{"fixture-model"}, Credentials: []string{"fixture-credential"}}
	if _, err := w.db.SQL.Exec("UPDATE runs SET assignment=? WHERE id=?", enc(a), a.AttemptId); err != nil {
		t.Fatal(err)
	}
	w.execute(context.Background(), a)
	var raw, runtimeRef []byte
	if err := w.db.SQL.QueryRow("SELECT completion,runtime_ref FROM runs WHERE id=?", a.AttemptId).Scan(&raw, &runtimeRef); err != nil {
		t.Fatal(err)
	}
	done := new(pb.CompleteRequest)
	if err := dec(raw, done); err != nil {
		t.Fatal(err)
	}
	if done.Success || done.ErrorCode != "ENVIRONMENT_ISOLATION_UNSUPPORTED" || len(runtimeRef) != 0 {
		t.Fatalf("host execution escaped isolation fence: completion=%+v runtime_ref=%q", done, runtimeRef)
	}
}

func TestEnvironmentCleanupUnknownFailsClosed(t *testing.T) {
	w, a, closeFn := environmentAssignment(t, "attempt", true)
	defer closeFn()

	w.execute(context.Background(), a)

	var completion []byte
	var state, cleanup, workspaceState string
	if err := w.db.SQL.QueryRow("SELECT completion,environment_state,environment_cleanup FROM runs WHERE id=?", a.AttemptId).
		Scan(&completion, &state, &cleanup); err != nil {
		t.Fatal(err)
	}
	if err := w.db.SQL.QueryRow("SELECT state FROM workspaces WHERE attempt=?", a.AttemptId).Scan(&workspaceState); err != nil {
		t.Fatal(err)
	}
	done := new(pb.CompleteRequest)
	if err := dec(completion, done); err != nil {
		t.Fatal(err)
	}
	if done.Success || done.CleanupConfirmed || done.ErrorCode != "CLEANUP_UNCONFIRMED" {
		t.Fatalf("unsafe completion=%+v", done)
	}
	if state != string(envreg.StateUnknown) || cleanup != string(envreg.CleanupUnknown) {
		t.Fatalf("environment state=%s cleanup=%s", state, cleanup)
	}
	if workspaceState != "QUARANTINED" {
		t.Fatalf("workspace state=%s", workspaceState)
	}
}

func TestRecoveryRequiresEnvironmentCleanupProof(t *testing.T) {
	ensureEnvironmentExecutionFixtures(t)
	for _, tc := range []struct {
		id    string
		clean bool
	}{
		{"recovery-clean", true},
		{"unknown-env-recovery", false},
	} {
		dir := t.TempDir()
		db, err := store.Open(dir, store.WorkerSchema)
		if err != nil {
			t.Fatal(err)
		}
		w := &Worker{
			db: db,
			cfg: config.Worker{
				DataDir:              dir,
				StopGraceMS:          50,
				WorkspaceRetentionMS: 1000,
				Runtimes: map[string]config.Runtime{
					"environment_remote_fixture": {Version: "fixture-v1"},
				},
			},
		}
		a := &pb.Assignment{
			TaskId:     "task-" + tc.id,
			AttemptId:  tc.id,
			Generation: 1,
			LeaseToken: "token",
			Spec: &pb.TaskSpec{
				RuntimeProfile:       "environment_remote_fixture",
				RequiredCapabilities: []string{"environment:" + fixtureEnvironmentName},
			},
		}
		runtimeRef, _ := adapter.EncodeExecutionRef(adapter.ExecutionRef{
			Provider: "environment_remote_fixture", Transport: "remote_api", ID: "exited",
		})
		envRef, _ := envreg.EncodeRef(envreg.Ref{Provider: fixtureEnvironmentName, ID: tc.id})
		if _, err = db.SQL.Exec(`INSERT INTO runs(
			id,assignment,state,runtime_provider,runtime_transport,runtime_ref,runtime_state,runtime_cleanup,
			environment_provider,environment_ref,environment_state,environment_cleanup
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			tc.id, enc(a), "RUNNING",
			"environment_remote_fixture", "remote_api", runtimeRef, string(adapter.RuntimeExited), string(adapter.CleanupConfirmed),
			fixtureEnvironmentName, envRef, string(envreg.StateActive), string(envreg.CleanupPending)); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err = w.recover(context.Background()); err != nil {
			db.Close()
			t.Fatal(err)
		}
		var completion []byte
		if err = db.SQL.QueryRow("SELECT completion FROM runs WHERE id=?", tc.id).Scan(&completion); err != nil {
			db.Close()
			t.Fatal(err)
		}
		done := new(pb.CompleteRequest)
		if err = dec(completion, done); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if done.CleanupConfirmed != tc.clean {
			db.Close()
			t.Fatalf("%s cleanup=%v want=%v completion=%+v", tc.id, done.CleanupConfirmed, tc.clean, done)
		}
		db.Close()
	}
}
