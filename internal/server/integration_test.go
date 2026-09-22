package server_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/maintenance"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/server"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"github.com/tommyxie2026-tech/computecloud/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestTwoWorkersLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	repo, commit := testutil.Repository(t)
	exe := testutil.CLI(t)
	userToken := testutil.Token(t, "user")
	w1Token := testutil.Token(t, "worker-1")
	w2Token := testutil.Token(t, "worker-2")
	cfg := config.Server{Listen: "127.0.0.1:0", DataDir: t.TempDir(), TLS: config.TLS{InsecureLoopback: true}, LeaseSeconds: 6, TickMS: 30, MaxArtifactBytes: 32 << 20, MaxProjectTasks: 8, Models: map[string]string{"codex_exec": "fixture-codex", "claude_print": "fixture-claude"}, Credentials: map[string]int{"account": 2}, Users: []config.Identity{{TokenFile: userToken, Owner: "owner", Projects: []string{"project"}, Credentials: []string{"account"}}}, Workers: []config.Identity{{TokenFile: w1Token, WorkerID: "worker-1", Projects: []string{"project"}, Credentials: []string{"account"}}, {TokenFile: w2Token, WorkerID: "worker-2", Projects: []string{"project"}, Credentials: []string{"account"}}}}
	s, e := server.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", cfg.Listen)
	if e != nil {
		t.Fatal(e)
	}
	serverCtx, serverStop := context.WithCancel(ctx)
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Serve(serverCtx, l) }()
	t.Cleanup(func() { serverStop(); <-serverDone; s.Close() })
	token, _ := config.Token(userToken)
	conn, e := rpcutil.Dial(l.Addr().String(), token, cfg.TLS)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	client := pb.NewRuntimeServiceClient(conn)
	spec := func(key, engine, prompt string) *pb.TaskSpec {
		return &pb.TaskSpec{ProjectId: "project", IdempotencyKey: key, Engine: engine, CredentialRef: "account", Workspace: &pb.Workspace{RepositoryRef: "repo", BaseCommit: commit}, Input: &pb.Input{Text: prompt}, PolicyRef: "trusted", AcceptanceProfile: "file-exists", TimeoutSeconds: 40, RequiredCapabilities: []string{"event_stream", "cancel"}}
	}
	// Queued cancellation is durable and prevents later dispatch.
	queued, e := client.SubmitTask(ctx, spec("queued", "codex", "hello"))
	if e != nil {
		t.Fatal(e)
	}
	queued, e = client.CancelTask(ctx, &pb.CancelRequest{TaskId: queued.TaskId, ControlId: "cancel-queued"})
	if e != nil || queued.State != "CANCELED" {
		t.Fatalf("queued cancel: %v %v", queued, e)
	}
	workerDirs := map[string]string{}
	for i, tokenPath := range []string{w1Token, w2Token} {
		id := []string{"worker-1", "worker-2"}[i]
		wc := config.Worker{ID: id, Address: l.Addr().String(), DataDir: t.TempDir(), TokenFile: tokenPath, TLS: cfg.TLS, Slots: 1, StopGraceMS: 100, Repositories: map[string]string{"repo": repo}, Policies: map[string]config.Policy{"trusted": {CodexSandbox: "workspace-write", ClaudePermissionMode: "dontAsk", ClaudeAllowedTools: []string{"Read", "Edit"}}}, Verifiers: map[string][][]string{"file-exists": {{"test", "-f", "result.txt"}}, "reject": {{"false"}}}, Runtimes: map[string]config.Runtime{"codex_exec": {Executable: exe, Version: "fixture-1", Models: []string{"fixture-codex"}, Credentials: []string{"account"}}, "claude_print": {Executable: exe, Version: "fixture-1", Models: []string{"fixture-claude"}, Credentials: []string{"account"}}}}
		workerDirs[id] = wc.DataDir
		w, e := worker.New(wc)
		if e != nil {
			t.Fatal(e)
		}
		wctx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- w.Run(wctx) }()
		t.Cleanup(func() {
			stop()
			if e := <-done; e != nil {
				t.Error(e)
			}
			w.Close()
		})
	}
	eventually(t, ctx, func() bool {
		r, e := client.ListWorkers(ctx, &pb.Empty{})
		return e == nil && len(r.Workers) == 2 && r.Workers[0].Online && r.Workers[1].Online
	})
	// Idempotency, conflicting payload and unsupported live-session capability.
	in := spec("success-0", "codex", "hello")
	task, e := client.SubmitTask(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	dup, e := client.SubmitTask(ctx, in)
	if e != nil || dup.TaskId != task.TaskId || !dup.Existing {
		t.Fatalf("duplicate: %v %v", dup, e)
	}
	changed := proto.Clone(in).(*pb.TaskSpec)
	changed.Input.Text = "changed"
	_, e = client.SubmitTask(ctx, changed)
	if status.Code(e) != codes.AlreadyExists {
		t.Fatalf("expected conflict: %v", e)
	}
	unsupported := spec("unsupported", "codex", "hello")
	unsupported.SessionRef = "session"
	_, e = client.SubmitTask(ctx, unsupported)
	if status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("resume capability: %v", e)
	}
	tasks := []*pb.Task{task}
	for i, engine := range []string{"claude", "codex", "claude"} {
		q, e := client.SubmitTask(ctx, spec([]string{"success-1", "success-2", "success-3"}[i], engine, "hello"))
		if e != nil {
			t.Fatal(e)
		}
		tasks = append(tasks, q)
	}
	used := map[string]bool{}
	for _, q := range tasks {
		var got *pb.Task
		eventually(t, ctx, func() bool {
			got, e = client.GetTask(ctx, &pb.TaskRef{TaskId: q.TaskId})
			return e == nil && (got.State == "SUCCEEDED" || got.State == "FAILED")
		})
		if got.State != "SUCCEEDED" {
			t.Fatalf("execution failed: %v", got)
		}
		used[got.WorkerId] = true
		arts, e := client.ListArtifacts(ctx, &pb.TaskRef{TaskId: q.TaskId})
		if e != nil || len(arts.Artifacts) != 1 {
			t.Fatalf("artifacts: %v %v", arts, e)
		}
		stream, e := client.DownloadArtifact(ctx, &pb.ArtifactRef{TaskId: q.TaskId, ArtifactId: arts.Artifacts[0].ArtifactId})
		if e != nil {
			t.Fatal(e)
		}
		var bytes []byte
		for {
			c, e := stream.Recv()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			bytes = append(bytes, c.Data...)
		}
		if store.Hash(bytes) != arts.Artifacts[0].Sha256 {
			t.Fatal("artifact hash mismatch")
		}
		events, e := client.WatchEvents(ctx, &pb.WatchRequest{TaskId: q.TaskId})
		if e != nil {
			t.Fatal(e)
		}
		var seq int64
		for {
			ev, e := events.Recv()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			if ev.Seq != seq+1 {
				t.Fatalf("event gap %d -> %d", seq, ev.Seq)
			}
			seq = ev.Seq
		}
		if seq != got.LastSeq {
			t.Fatal("terminal replay watermark mismatch")
		}
	}
	if len(used) != 2 {
		t.Fatalf("both workers must execute: %v", used)
	}
	// A native success must not bypass the trusted acceptance command.
	rejectedSpec := spec("verification-rejected", "codex", "hello")
	rejectedSpec.AcceptanceProfile = "reject"
	rejected, e := client.SubmitTask(ctx, rejectedSpec)
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, ctx, func() bool {
		rejected, e = client.GetTask(ctx, &pb.TaskRef{TaskId: rejected.TaskId})
		return e == nil && (rejected.State == "FAILED" || rejected.State == "SUCCEEDED")
	})
	if rejected.State != "FAILED" || rejected.ErrorCode != "VERIFICATION_FAILED" {
		t.Fatalf("verification bypassed: %v", rejected)
	}
	// Missing final must fail even with an exit code of zero.
	missing, e := client.SubmitTask(ctx, spec("missing", "codex", "missing"))
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, ctx, func() bool {
		missing, e = client.GetTask(ctx, &pb.TaskRef{TaskId: missing.TaskId})
		return e == nil && missing.State == "FAILED"
	})
	if missing.ErrorCode != "MISSING_FINAL" {
		t.Fatalf("missing final accepted: %v", missing)
	}
	slow, e := client.SubmitTask(ctx, spec("slow", "codex", "slow"))
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, ctx, func() bool {
		slow, e = client.GetTask(ctx, &pb.TaskRef{TaskId: slow.TaskId})
		return e == nil && slow.State == "RUNNING"
	})
	_, e = client.CancelTask(ctx, &pb.CancelRequest{TaskId: slow.TaskId, ControlId: "stop-slow"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, ctx, func() bool {
		slow, e = client.GetTask(ctx, &pb.TaskRef{TaskId: slow.TaskId})
		return e == nil && slow.State == "CANCELED"
	})
	pidFile := filepath.Join(workerDirs[slow.WorkerId], "workspaces", slow.AttemptId, "descendant.pid")
	if b, e := os.ReadFile(pidFile); e == nil {
		pid := strings.TrimSpace(string(b))
		if b, e := os.ReadFile("/proc/" + pid + "/stat"); e == nil && !strings.Contains(string(b), ") Z ") {
			t.Fatalf("live descendant remains: %s", b)
		}
	}
	if e := maintenance.Backup(ctx, cfg.DataDir, filepath.Join(t.TempDir(), "backup.tar.gz")); e == nil {
		t.Fatal("online backup should be refused")
	}
}
func eventually(t *testing.T, ctx context.Context, p func() bool) {
	t.Helper()
	for {
		if p() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("condition not met: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
