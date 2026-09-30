package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
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
	if alive, err := process.Alive(cmd.Process.Pid); err != nil || alive {
		t.Fatalf("process survived recovery or inspection failed: alive=%v err=%v", alive, err)
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


type recoveryRemoteParser struct{}

func (recoveryRemoteParser) Line([]byte) error { return nil }
func (recoveryRemoteParser) Outcome() adapter.Outcome {
	return adapter.Outcome{Final: true, Success: true}
}

type recoveryRemoteProvider struct{}

func (recoveryRemoteProvider) Profile() string { return "recovery_remote_fixture" }
func (recoveryRemoteProvider) Version(r config.Runtime) string { return r.Version }
func (recoveryRemoteProvider) Transport() string { return "remote_api" }
func (recoveryRemoteProvider) Probe(context.Context, config.Runtime) error { return nil }
func (recoveryRemoteProvider) Args(*pb.TaskSpec, config.Policy) ([]string, error) { return nil, nil }
func (recoveryRemoteProvider) Parser(func(string, []byte) error) adapter.StreamParser { return recoveryRemoteParser{} }
func (recoveryRemoteProvider) Prepare(req adapter.PrepareRequest) (adapter.PreparedExecution, error) {
	return adapter.PreparedExecution{Profile: "recovery_remote_fixture", Runtime: req.Runtime}, nil
}
func (recoveryRemoteProvider) Start(_ context.Context, _ adapter.PreparedExecution, started func(adapter.ExecutionRef) error) adapter.StartResult {
	ref := adapter.ExecutionRef{Provider: "recovery_remote_fixture", Transport: "remote_api", ID: "exited"}
	if started != nil {
		if err := started(ref); err != nil {
			return adapter.StartResult{Ref: ref, State: adapter.RuntimeUnknown, Cleanup: adapter.CleanupUnknown, Err: err}
		}
	}
	return adapter.StartResult{Ref: ref, State: adapter.RuntimeExited, Cleanup: adapter.CleanupConfirmed, Outcome: adapter.Outcome{Final: true, Success: true}}
}
func (recoveryRemoteProvider) Inspect(_ context.Context, _ config.Runtime, ref adapter.ExecutionRef) (adapter.Inspection, error) {
	switch ref.ID {
	case "running":
		return adapter.Inspection{State: adapter.RuntimeRunning, Cleanup: adapter.CleanupPending}, nil
	case "exited":
		return adapter.Inspection{State: adapter.RuntimeExited, Cleanup: adapter.CleanupConfirmed}, nil
	default:
		return adapter.Inspection{State: adapter.RuntimeUnknown, Cleanup: adapter.CleanupUnknown}, nil
	}
}
func (recoveryRemoteProvider) Stop(_ context.Context, _ config.Runtime, ref adapter.ExecutionRef, _ time.Duration) (adapter.StopResult, error) {
	if ref.ID == "running" {
		return adapter.StopResult{State: adapter.RuntimeExited, Cleanup: adapter.CleanupConfirmed}, nil
	}
	return adapter.StopResult{State: adapter.RuntimeUnknown, Cleanup: adapter.CleanupUnknown}, nil
}
func (recoveryRemoteProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{Runtime: []string{"remote_api"}}
}
func (recoveryRemoteProvider) SupportsGateway() bool { return false }

func TestRecoveryUsesRuntimeProviderInspectAndStop(t *testing.T) {
	if _, ok := adapter.Lookup("recovery_remote_fixture"); !ok {
		if err := adapter.Register(recoveryRemoteProvider{}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := store.Open(t.TempDir(), store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	w := &Worker{
		db: d,
		cfg: config.Worker{
			StopGraceMS: 50,
			Runtimes: map[string]config.Runtime{
				"recovery_remote_fixture": {Version: "fixture-v1"},
			},
		},
	}
	for _, tc := range []struct {
		id      string
		cleanup bool
	}{
		{"running", true},
		{"unknown", false},
	} {
		a := &pb.Assignment{
			TaskId: tc.id, AttemptId: tc.id, Generation: 1, LeaseToken: "token",
			Spec: &pb.TaskSpec{RuntimeProfile: "recovery_remote_fixture"},
		}
		refBytes, err := adapter.EncodeExecutionRef(adapter.ExecutionRef{
			Provider: "recovery_remote_fixture", Transport: "remote_api", ID: tc.id,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.SQL.Exec(`INSERT INTO runs(
			id,assignment,state,runtime_provider,runtime_transport,runtime_ref,runtime_state,runtime_cleanup
		) VALUES(?,?,?,?,?,?,?,?)`,
			tc.id, enc(a), "RUNNING", "recovery_remote_fixture", "remote_api", refBytes,
			string(adapter.RuntimeRunning), string(adapter.CleanupPending)); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id      string
		cleanup bool
	}{
		{"running", true},
		{"unknown", false},
	} {
		var b []byte
		var state, cleanup string
		if err = d.SQL.QueryRow("SELECT completion,runtime_state,runtime_cleanup FROM runs WHERE id=?", tc.id).
			Scan(&b, &state, &cleanup); err != nil {
			t.Fatal(err)
		}
		done := new(pb.CompleteRequest)
		if err = dec(b, done); err != nil {
			t.Fatal(err)
		}
		if done.CleanupConfirmed != tc.cleanup {
			t.Fatalf("%s cleanup=%v want=%v state=%s runtime_cleanup=%s", tc.id, done.CleanupConfirmed, tc.cleanup, state, cleanup)
		}
		if tc.cleanup {
			if state != string(adapter.RuntimeExited) || cleanup != string(adapter.CleanupConfirmed) {
				t.Fatalf("%s runtime state=%s cleanup=%s", tc.id, state, cleanup)
			}
		} else if state != string(adapter.RuntimeUnknown) || cleanup != string(adapter.CleanupUnknown) {
			t.Fatalf("%s unknown runtime was not fail-closed: state=%s cleanup=%s", tc.id, state, cleanup)
		}
	}
}
