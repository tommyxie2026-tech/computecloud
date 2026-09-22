package server_test

import (
	"context"
	"net"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/server"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestReplayEventIntegrityAndExpiredLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userToken := testutil.Token(t, "u")
	workerToken := testutil.Token(t, "w")
	cfg := config.Server{DataDir: t.TempDir(), TLS: config.TLS{InsecureLoopback: true}, LeaseSeconds: 3, TickMS: 20, MaxArtifactBytes: 1 << 20, MaxProjectTasks: 2, Credentials: map[string]int{"account": 1}, Models: map[string]string{"codex_exec": "model"}, Users: []config.Identity{{TokenFile: userToken, Owner: "user", Projects: []string{"p"}, Credentials: []string{"account"}}}, Workers: []config.Identity{{TokenFile: workerToken, WorkerID: "w", Projects: []string{"p"}, Credentials: []string{"account"}}}}
	s, e := server.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	sc, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Serve(sc, l) }()
	defer func() { stop(); <-done; s.Close() }()
	dial := func(tokenFile string) *grpc.ClientConn {
		v, _ := config.Token(tokenFile)
		c, e := rpcutil.Dial(l.Addr().String(), v, cfg.TLS)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	user := pb.NewRuntimeServiceClient(dial(userToken))
	node := pb.NewRuntimeServiceClient(dial(workerToken))
	hello := &pb.WorkerHello{WorkerId: "w", Epoch: "epoch", Slots: 1, Runtimes: []*pb.Runtime{{Profile: "codex_exec", Models: []string{"model"}, Credentials: []string{"account"}, Repositories: []string{"repo"}, Policies: []string{"policy"}, Verifiers: []string{"verify"}, Capabilities: []string{"event_stream", "cancel"}}}}
	connect := func() (grpc.BidiStreamingClient[pb.WorkerFrame, pb.ServerFrame], context.CancelFunc) {
		c, stop := context.WithCancel(ctx)
		st, e := node.ConnectWorker(c)
		if e != nil {
			t.Fatal(e)
		}
		if e = st.Send(&pb.WorkerFrame{Body: &pb.WorkerFrame_Hello{Hello: hello}}); e != nil {
			t.Fatal(e)
		}
		return st, stop
	}
	stream, disconnect := connect()
	defer disconnect()
	task, e := user.SubmitTask(ctx, &pb.TaskSpec{ProjectId: "p", IdempotencyKey: "one", Engine: "codex", CredentialRef: "account", Workspace: &pb.Workspace{RepositoryRef: "repo", BaseCommit: "0123456789abcdef0123456789abcdef01234567"}, Input: &pb.Input{Text: "test"}, PolicyRef: "policy", AcceptanceProfile: "verify", TimeoutSeconds: 15})
	if e != nil {
		t.Fatal(e)
	}
	f, e := stream.Recv()
	if e != nil {
		t.Fatal(e)
	}
	command := f.GetCommand()
	if command == nil || command.Kind != "start" {
		t.Fatalf("expected start: %v", f)
	}
	a := command.Assignment
	ref := &pb.AttemptRef{AttemptId: a.AttemptId, Generation: a.Generation, LeaseToken: a.LeaseToken}
	event := &pb.Event{TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: a.Generation, EventId: "event-1", WorkerSeq: 1, Type: "attempt.started", PayloadJson: []byte(`{}`)}
	for i := 0; i < 2; i++ {
		ack, e := node.ReportEvents(ctx, &pb.ReportRequest{Attempt: ref, Events: []*pb.Event{event}})
		if e != nil || ack.ThroughSeq != 1 {
			t.Fatalf("event replay: %v %v", ack, e)
		}
	}
	bad := proto.Clone(event).(*pb.Event)
	bad.PayloadJson = []byte(`{"changed":true}`)
	if _, e = node.ReportEvents(ctx, &pb.ReportRequest{Attempt: ref, Events: []*pb.Event{bad}}); status.Code(e) != codes.AlreadyExists {
		t.Fatalf("event conflict: %v", e)
	}
	gap := proto.Clone(event).(*pb.Event)
	gap.WorkerSeq = 3
	gap.EventId = "event-3"
	if _, e = node.ReportEvents(ctx, &pb.ReportRequest{Attempt: ref, Events: []*pb.Event{gap}}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("event gap: %v", e)
	}
	forged := proto.Clone(ref).(*pb.AttemptRef)
	forged.LeaseToken = "wrong"
	if _, e = node.ReportEvents(ctx, &pb.ReportRequest{Attempt: forged}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("stale token: %v", e)
	}
	// Drop the start ACK and reconnect. Same durable command is replayed.
	disconnect()
	stream, disconnect = connect()
	defer disconnect()
	f, e = stream.Recv()
	if e != nil {
		t.Fatal(e)
	}
	if f.GetCommand() == nil || f.GetCommand().CommandId != command.CommandId {
		t.Fatalf("command was recreated instead of replayed: %v", f)
	}
	// No lease renewals. Lost execution must not free its slot automatically.
	eventually(t, ctx, func() bool {
		task, e = user.GetTask(ctx, &pb.TaskRef{TaskId: task.TaskId})
		return e == nil && task.State == "RECONCILING"
	})
	blockedSpec := proto.Clone(task.Spec).(*pb.TaskSpec)
	blockedSpec.IdempotencyKey = "blocked"
	blocked, e := user.SubmitTask(ctx, blockedSpec)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	blocked, e = user.GetTask(ctx, &pb.TaskRef{TaskId: blocked.TaskId})
	if e != nil || blocked.State != "QUEUED" {
		t.Fatalf("lost slot released prematurely: %v %v", blocked, e)
	}
	noProof := &pb.CompleteRequest{Attempt: ref, FinalWorkerSeq: 1, ErrorCode: "WORKER_LOST"}
	ack, e := node.CompleteAttempt(ctx, noProof)
	if e != nil || ack.State != "RECONCILING" {
		t.Fatalf("unconfirmed cleanup: %v %v", ack, e)
	}
	noProof.CleanupConfirmed = true
	ack, e = node.CompleteAttempt(ctx, noProof)
	if e != nil || ack.State != "COMMITTED" {
		t.Fatalf("confirmed cleanup: %v %v", ack, e)
	}
	if _, e = node.CompleteAttempt(ctx, noProof); e != nil {
		t.Fatal("completion retry failed", e)
	}
	conflict := proto.Clone(noProof).(*pb.CompleteRequest)
	conflict.ErrorCode = "DIFFERENT"
	if _, e = node.CompleteAttempt(ctx, conflict); status.Code(e) != codes.AlreadyExists {
		t.Fatalf("completion conflict: %v", e)
	}
	task, e = user.GetTask(ctx, &pb.TaskRef{TaskId: task.TaskId})
	if e != nil || task.State != "FAILED" {
		t.Fatalf("lost task: %v %v", task, e)
	}
	if _, e = node.GetTask(ctx, &pb.TaskRef{TaskId: task.TaskId}); status.Code(e) != codes.PermissionDenied {
		t.Fatalf("worker used user API: %v", e)
	}
	if _, e = user.ReportEvents(ctx, &pb.ReportRequest{Attempt: ref}); status.Code(e) != codes.PermissionDenied {
		t.Fatalf("user used worker API: %v", e)
	}
}
