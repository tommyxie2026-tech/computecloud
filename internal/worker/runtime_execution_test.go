package worker

import (
	"context"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestRemoteRuntimeExecutesWithoutLocalAgentProcess(t *testing.T) {
	if _, ok := adapter.Lookup("recovery_remote_fixture"); !ok {
		if err := adapter.Register(recoveryRemoteProvider{}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	db, err := store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, commit := workspaceTestRepo(t)
	w := &Worker{
		db: db,
		cfg: config.Worker{
			DataDir: dir,
			StopGraceMS: 50,
			WorkspaceRetentionMS: 1000,
			Runtimes: map[string]config.Runtime{
				"recovery_remote_fixture": {
					Version: "fixture-v1",
					Models: []string{"fixture-model"},
					Credentials: []string{"fixture-credential"},
				},
			},
			Repositories: map[string]string{"repo": repo},
			Policies: map[string]config.Policy{"policy": {}},
			Verifiers: map[string][][]string{"accept": {}},
		},
	}
	a := &pb.Assignment{
		TaskId: "remote-task",
		AttemptId: "remote-attempt",
		Generation: 1,
		LeaseToken: "remote-token",
		Spec: &pb.TaskSpec{
			RuntimeProfile: "recovery_remote_fixture",
			Model: "fixture-model",
			CredentialRef: "fixture-credential",
			PolicyRef: "policy",
			AcceptanceProfile: "accept",
			Workspace: &pb.Workspace{RepositoryRef: "repo", BaseCommit: commit},
			Input: &pb.Input{Text: "remote fixture"},
		},
	}
	if _, err = db.SQL.Exec("INSERT INTO runs(id,assignment,state) VALUES(?,?,?)", a.AttemptId, enc(a), "ACCEPTED"); err != nil {
		t.Fatal(err)
	}

	w.execute(context.Background(), a)

	var completion []byte
	var provider, transport, state, cleanup string
	var pid int
	var runtimeRef []byte
	if err = db.SQL.QueryRow(`SELECT completion,runtime_provider,runtime_transport,runtime_ref,
		runtime_state,runtime_cleanup,pid FROM runs WHERE id=?`, a.AttemptId).
		Scan(&completion, &provider, &transport, &runtimeRef, &state, &cleanup, &pid); err != nil {
		t.Fatal(err)
	}
	done := new(pb.CompleteRequest)
	if err = dec(completion, done); err != nil {
		t.Fatal(err)
	}
	if !done.Success || !done.CleanupConfirmed {
		t.Fatalf("remote completion=%+v", done)
	}
	if provider != "recovery_remote_fixture" || transport != "remote_api" || pid != 0 {
		t.Fatalf("runtime provider=%s transport=%s pid=%d", provider, transport, pid)
	}
	ref, err := adapter.DecodeExecutionRef(runtimeRef)
	if err != nil {
		t.Fatal(err)
	}
	if ref.PID != 0 || ref.StartID != "" || ref.Transport != "remote_api" {
		t.Fatalf("remote ref contains local process identity: %+v", ref)
	}
	if state != string(adapter.RuntimeExited) || cleanup != string(adapter.CleanupConfirmed) {
		t.Fatalf("runtime state=%s cleanup=%s", state, cleanup)
	}
}
