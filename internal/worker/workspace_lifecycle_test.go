package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func workspaceTestRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "fixture")
	return dir, run("rev-parse", "HEAD")
}

func workspaceAssignment(id, task string, generation int64, commit string) *pb.Assignment {
	return &pb.Assignment{
		TaskId: idOr(task, "task"),
		AttemptId: id,
		Generation: generation,
		LeaseToken: "lease-" + id,
		Spec: &pb.TaskSpec{
			Workspace: &pb.Workspace{RepositoryRef: "repo", BaseCommit: commit},
		},
	}
}

func idOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func insertWorkspaceRun(t *testing.T, w *Worker, a *pb.Assignment, state string) {
	t.Helper()
	if _, err := w.db.SQL.Exec("INSERT INTO runs(id,assignment,state) VALUES(?,?,?)", a.AttemptId, enc(a), state); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLifecycleOwnershipRetentionAndGC(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, commit := workspaceTestRepo(t)
	w := &Worker{db: db, cfg: config.Worker{
		DataDir: dir,
		Repositories: map[string]string{"repo": repo},
		WorkspaceRetentionMS: 1,
	}}
	a := workspaceAssignment("attempt-one", "task", 1, commit)
	insertWorkspaceRun(t, w, a, "ACCEPTED")

	cwd, err := w.prepareWorkspace(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.SQL.QueryRow("SELECT state FROM workspaces WHERE attempt=?", a.AttemptId).Scan(&state); err != nil || state != "READY" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	if err = w.markWorkspaceInUse(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	inputDir := filepath.Join(dir, "inputs", a.AttemptId)
	if err = os.MkdirAll(inputDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(inputDir, "manifest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.completion(context.Background(), a, &pb.CompleteRequest{Success: true, CleanupConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(inputDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt inputs survived cleanup-confirmed completion: %v", err)
	}
	var retainUntil int64
	if err = db.SQL.QueryRow("SELECT state,retain_until FROM workspaces WHERE attempt=?", a.AttemptId).Scan(&state, &retainUntil); err != nil || state != "RETAINED" || retainUntil == 0 {
		t.Fatalf("state=%s retain_until=%d err=%v", state, retainUntil, err)
	}

	if _, err = db.SQL.Exec("UPDATE workspaces SET retain_until=? WHERE attempt=?", store.Now()-1, a.AttemptId); err != nil {
		t.Fatal(err)
	}
	if err = w.reconcileWorkspaceLifecycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cwd); err != nil {
		t.Fatalf("workspace deleted before Server completion: %v", err)
	}

	if _, err = db.SQL.Exec("UPDATE runs SET completed=1 WHERE id=?", a.AttemptId); err != nil {
		t.Fatal(err)
	}
	if err = w.reconcileWorkspaceLifecycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	var deletedAt int64
	if err = db.SQL.QueryRow("SELECT state,deleted_at FROM workspaces WHERE attempt=?", a.AttemptId).Scan(&state, &deletedAt); err != nil || state != "DELETED" || deletedAt == 0 {
		t.Fatalf("state=%s deleted_at=%d err=%v", state, deletedAt, err)
	}
	if _, err = os.Stat(cwd); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace still exists: %v", err)
	}
}

func TestWorkspaceGenerationIsolationAndOwnership(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, commit := workspaceTestRepo(t)
	w := &Worker{db: db, cfg: config.Worker{DataDir: dir, Repositories: map[string]string{"repo": repo}}}

	a1 := workspaceAssignment("attempt-g1", "same-task", 1, commit)
	a2 := workspaceAssignment("attempt-g2", "same-task", 2, commit)
	for _, a := range []*pb.Assignment{a1, a2} {
		insertWorkspaceRun(t, w, a, "ACCEPTED")
	}
	p1, err := w.prepareWorkspace(context.Background(), a1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := w.prepareWorkspace(context.Background(), a2)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatal("two generations shared a writable workspace")
	}
	conflict := workspaceAssignment(a1.AttemptId, a1.TaskId, 9, commit)
	if _, err = w.prepareWorkspace(context.Background(), conflict); err == nil {
		t.Fatal("attempt workspace ownership was rebound to another generation")
	}
}

func TestWorkspaceRecoveryUsesPreSpawnProofAndQuarantinesUnknown(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, commit := workspaceTestRepo(t)
	w := &Worker{db: db, cfg: config.Worker{DataDir: dir, Repositories: map[string]string{"repo": repo}, StopGraceMS: 10}}

	for _, tc := range []struct {
		id, wsState string
		clean       bool
	}{
		{"safe-pre-spawn", "READY", true},
		{"unknown-spawn", "IN_USE", false},
	} {
		a := workspaceAssignment(tc.id, tc.id, 1, commit)
		insertWorkspaceRun(t, w, a, "STARTING")
		inputDir := filepath.Join(dir, "inputs", tc.id)
		if err = os.MkdirAll(inputDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(inputDir, "bundle"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "workspaces", tc.id)
		if err = os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		now := store.Now()
		if _, err = db.SQL.Exec(`INSERT INTO workspaces(
			attempt,task,generation,repository_ref,base_commit,path,state,created,updated
		) VALUES(?,?,?,?,?,?,?,?,?)`, tc.id, tc.id, int64(1), "repo", commit, tc.id, tc.wsState, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, wsState string
		clean       bool
	}{
		{"safe-pre-spawn", "RETAINED", true},
		{"unknown-spawn", "QUARANTINED", false},
	} {
		var completion []byte
		var state string
		if err = db.SQL.QueryRow("SELECT completion FROM runs WHERE id=?", tc.id).Scan(&completion); err != nil {
			t.Fatal(err)
		}
		done := new(pb.CompleteRequest)
		if err = dec(completion, done); err != nil {
			t.Fatal(err)
		}
		if done.CleanupConfirmed != tc.clean {
			t.Fatalf("%s cleanup=%v want=%v", tc.id, done.CleanupConfirmed, tc.clean)
		}
		if err = db.SQL.QueryRow("SELECT state FROM workspaces WHERE attempt=?", tc.id).Scan(&state); err != nil || state != tc.wsState {
			t.Fatalf("%s state=%s err=%v", tc.id, state, err)
		}
		_, inputErr := os.Stat(filepath.Join(dir, "inputs", tc.id))
		if tc.clean && !errors.Is(inputErr, os.ErrNotExist) {
			t.Fatalf("%s safe recovery kept attempt inputs: %v", tc.id, inputErr)
		}
		if !tc.clean && inputErr != nil {
			t.Fatalf("%s quarantined recovery removed attempt inputs: %v", tc.id, inputErr)
		}
	}
	if _, err = db.SQL.Exec("UPDATE runs SET completed=1 WHERE id='unknown-spawn'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec("UPDATE workspaces SET retain_until=? WHERE attempt='unknown-spawn'", store.Now()-1); err != nil {
		t.Fatal(err)
	}
	if err = w.reconcileWorkspaceLifecycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.SQL.QueryRow("SELECT state FROM workspaces WHERE attempt='unknown-spawn'").Scan(&state); err != nil || state != "QUARANTINED" {
		t.Fatalf("quarantined workspace was GC eligible: state=%s err=%v", state, err)
	}
}

func TestWorkspaceBootstrapAdoptsOnlyKnownLegacyRuns(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, commit := workspaceTestRepo(t)
	w := &Worker{db: db, cfg: config.Worker{DataDir: dir, Repositories: map[string]string{"repo": repo}}}
	a := workspaceAssignment("legacy-run", "legacy-task", 1, commit)
	done := &pb.CompleteRequest{Attempt: ref(a), CleanupConfirmed: true}
	if _, err = db.SQL.Exec("INSERT INTO runs(id,assignment,state,completion,completed) VALUES(?,?,?,?,1)", a.AttemptId, enc(a), "DONE", enc(done)); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, "workspaces", a.AttemptId), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, "workspaces", "unowned"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = w.bootstrapWorkspaceInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.SQL.QueryRow("SELECT state FROM workspaces WHERE attempt=?", a.AttemptId).Scan(&state); err != nil || state != "RETAINED" {
		t.Fatalf("legacy state=%s err=%v", state, err)
	}
	var n int
	if err = db.SQL.QueryRow("SELECT count(*) FROM workspaces WHERE attempt='unowned'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("unowned directory was adopted: n=%d err=%v", n, err)
	}
}
