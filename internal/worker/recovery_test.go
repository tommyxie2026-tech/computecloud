package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecoveryStopsRecordedProcessAndQuarantinesUnknownSpawn(t *testing.T) {
	d, e := store.Open(t.TempDir(), store.WorkerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	w := &Worker{db: d, cfg: config.Worker{StopGraceMS: 50}}
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	identity, e := process.Identity(cmd.Process.Pid)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		id, state string
		pid       int
		identity  string
	}{{"running", "RUNNING", cmd.Process.Pid, identity}, {"unknown", "STARTING", 0, ""}, {"accepted", "ACCEPTED", 0, ""}} {
		a := &pb.Assignment{TaskId: tc.id, AttemptId: tc.id, Generation: 1, LeaseToken: "token"}
		if _, e = d.SQL.Exec("INSERT INTO runs(id,assignment,state,pid,start_id) VALUES(?,?,?,?,?)", tc.id, enc(a), tc.state, tc.pid, tc.identity); e != nil {
			t.Fatal(e)
		}
	}
	if e = w.recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"running", "unknown", "accepted"} {
		var b []byte
		if e = d.SQL.QueryRow("SELECT completion FROM runs WHERE id=?", id).Scan(&b); e != nil {
			t.Fatal(e)
		}
		c := new(pb.CompleteRequest)
		if e = dec(b, c); e != nil {
			t.Fatal(e)
		}
		if c.CleanupConfirmed != (id != "unknown") || c.Success {
			t.Fatalf("unsafe recovery %s: %v", id, c)
		}
	}
	if b, e := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/stat"); e == nil && !strings.Contains(string(b), ") Z ") {
		t.Fatalf("process survived recovery: %s", b)
	}
}

type lostCompletionACK struct {
	pb.RuntimeServiceClient
	calls int
}

func (c *lostCompletionACK) CompleteAttempt(context.Context, *pb.CompleteRequest, ...grpc.CallOption) (*pb.Ack, error) {
	c.calls++
	if c.calls == 1 {
		return nil, status.Error(codes.Unavailable, "response lost after commit")
	}
	return &pb.Ack{State: "COMMITTED"}, nil
}
func TestCompletionACKLossDoesNotUploadAgain(t *testing.T) {
	d, e := store.Open(t.TempDir(), store.WorkerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	client := &lostCompletionACK{}
	w := &Worker{db: d, client: client}
	a := &pb.Assignment{TaskId: "task", AttemptId: "attempt", Generation: 1, LeaseToken: "token"}
	c := &pb.CompleteRequest{Attempt: ref(a), Success: true, CleanupConfirmed: true, ArtifactIds: []string{"already-uploaded"}}
	if _, e = d.SQL.Exec("INSERT INTO runs(id,assignment,state,completion,uploaded) VALUES(?,?,?,?,1)", a.AttemptId, enc(a), "DONE", enc(c)); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = w.flush(ctx); status.Code(e) != codes.Unavailable {
		t.Fatalf("lost ACK: %v", e)
	}
	if e = w.flush(ctx); e != nil {
		t.Fatal(e)
	}
	var completed bool
	if e = d.SQL.QueryRow("SELECT completed FROM runs WHERE id=?", a.AttemptId).Scan(&completed); e != nil || !completed || client.calls != 2 {
		t.Fatalf("retry did not finish: %v %v", completed, e)
	}
}

type rejectedArtifactClient struct {
	pb.RuntimeServiceClient
	completed *pb.CompleteRequest
}
type rejectedArtifactStream struct{ grpc.ClientStream }

func (*rejectedArtifactStream) Send(*pb.ArtifactChunk) error { return nil }
func (*rejectedArtifactStream) CloseAndRecv() (*pb.Artifact, error) {
	return nil, status.Error(codes.InvalidArgument, "artifact exceeds configured limit")
}
func (*rejectedArtifactClient) UploadArtifact(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[pb.ArtifactChunk, pb.Artifact], error) {
	return &rejectedArtifactStream{}, nil
}
func (c *rejectedArtifactClient) CompleteAttempt(_ context.Context, r *pb.CompleteRequest, _ ...grpc.CallOption) (*pb.Ack, error) {
	c.completed = r
	return &pb.Ack{State: "COMMITTED"}, nil
}

func TestRejectedArtifactFailsTaskWithoutHoldingCleanedExecution(t *testing.T) {
	dir := t.TempDir()
	d, e := store.Open(dir, store.WorkerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	client := &rejectedArtifactClient{}
	w := &Worker{db: d, client: client, cfg: config.Worker{DataDir: dir}}
	a := &pb.Assignment{TaskId: "task", AttemptId: "attempt", Generation: 1, LeaseToken: "token"}
	m, e := w.bundle(a, map[string][]byte{"report.json": []byte(`{"success":true}`)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, "artifacts", m.ArtifactId)); e != nil {
		t.Fatal(e)
	}
	c := &pb.CompleteRequest{Attempt: ref(a), Success: true, CleanupConfirmed: true, ArtifactIds: []string{m.ArtifactId}}
	if _, e = d.SQL.Exec("INSERT INTO runs(id,assignment,state,completion) VALUES(?,?,?,?)", a.AttemptId, enc(a), "DONE", enc(c)); e != nil {
		t.Fatal(e)
	}
	if e = w.flush(context.Background()); e != nil {
		t.Fatal(e)
	}
	if client.completed == nil || client.completed.Success || !client.completed.CleanupConfirmed || client.completed.ErrorCode != "ARTIFACT_ERROR" || len(client.completed.ArtifactIds) != 0 {
		t.Fatalf("bad completion: %v", client.completed)
	}
}
