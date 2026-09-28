package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type controlWorkerFixture struct {
	mu     sync.Mutex
	inputs int
	approvals int
	resumes int
}

func (p *controlWorkerFixture) Profile() string { return "control_worker_fixture" }
func (p *controlWorkerFixture) Version(config.Runtime) string { return "fixture" }
func (p *controlWorkerFixture) Transport() string { return "remote_api" }
func (p *controlWorkerFixture) Probe(context.Context, config.Runtime) error { return nil }
func (p *controlWorkerFixture) Args(*pb.TaskSpec, config.Policy) ([]string, error) { return nil, nil }
func (p *controlWorkerFixture) Parser(func(string, []byte) error) adapter.StreamParser { return nil }
func (p *controlWorkerFixture) Prepare(adapter.PrepareRequest) (adapter.PreparedExecution, error) {
	return adapter.PreparedExecution{}, nil
}
func (p *controlWorkerFixture) Start(context.Context, adapter.PreparedExecution, func(adapter.ExecutionRef) error) adapter.StartResult {
	return adapter.StartResult{}
}
func (p *controlWorkerFixture) Inspect(context.Context, config.Runtime, adapter.ExecutionRef) (adapter.Inspection, error) {
	return adapter.Inspection{State: adapter.RuntimeRunning, Cleanup: adapter.CleanupPending}, nil
}
func (p *controlWorkerFixture) Stop(context.Context, config.Runtime, adapter.ExecutionRef, time.Duration) (adapter.StopResult, error) {
	return adapter.StopResult{State: adapter.RuntimeExited, Cleanup: adapter.CleanupConfirmed}, nil
}
func (p *controlWorkerFixture) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{Runtime: []string{"event_stream", "cancel"}}
}
func (p *controlWorkerFixture) SupportsGateway() bool { return false }
func (p *controlWorkerFixture) ControlDescriptor() adapter.ControlDescriptor {
	return adapter.ControlDescriptor{
		ProtocolVersion: control.ProtocolV1Alpha1,
		Capabilities: []control.Capability{
			control.CapabilityInteractiveInput,
			control.CapabilityQueueNextInput,
			control.CapabilityInterrupt,
			control.CapabilityApproval,
			control.CapabilitySessionResume,
		},
	}
}
func (p *controlWorkerFixture) Resume(context.Context, adapter.ControlResumeRequest) (adapter.ExecutionRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resumes++
	return adapter.ExecutionRef{Provider: p.Profile(), Transport: "remote_api", ID: "runtime-resumed"}, nil
}
func (p *controlWorkerFixture) Input(_ context.Context, _ adapter.ControlInputRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs++
	return nil
}
func (p *controlWorkerFixture) Approve(context.Context, adapter.ControlApprovalRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approvals++
	return nil
}
func (p *controlWorkerFixture) Interrupt(context.Context, adapter.ControlInterruptRequest) error { return nil }

func TestAgentControlWorkerExecutesCommandAtMostOnce(t *testing.T) {
	d, err := store.Open(t.TempDir(), store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	provider := &controlWorkerFixture{}
	if err = adapter.Register(provider); err != nil {
		t.Fatal(err)
	}
	w := &Worker{db: d}
	a := &pb.Assignment{
		TaskId: "task-control", AttemptId: "attempt-control", Generation: 2, LeaseToken: "lease",
		Spec: &pb.TaskSpec{RuntimeProfile: provider.Profile()},
	}
	ref := adapter.ExecutionRef{Provider: provider.Profile(), Transport: "remote_api", ID: "runtime-1"}
	rawRef, err := adapter.EncodeExecutionRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.SQL.Exec("INSERT INTO runs(id,assignment,state,runtime_provider,runtime_transport,runtime_ref,runtime_state,runtime_cleanup) VALUES(?,?,?,?,?,?,?,?)",
		a.AttemptId, enc(a), "RUNNING", provider.Profile(), "remote_api", rawRef, string(adapter.RuntimeRunning), string(adapter.CleanupPending)); err != nil {
		t.Fatal(err)
	}
	cmd := &pb.Command{
		CommandId: "control-command-1",
		Kind: "control",
		Control: &pb.ControlCommand{
			PrincipalId: "owner", OperationId: "operation-1",
			TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: a.Generation,
			Action: "input", Mode: "queue_next", Input: &pb.Input{Text: "continue"},
		},
	}
	first, err := w.executeControl(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "COMPLETED" {
		t.Fatalf("first ack=%+v", first)
	}
	second, err := w.executeControl(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != "COMPLETED" {
		t.Fatalf("replay ack=%+v", second)
	}
	provider.mu.Lock()
	inputs := provider.inputs
	provider.mu.Unlock()
	if inputs != 1 {
		t.Fatalf("provider side effects=%d want=1", inputs)
	}
	var state string
	if err = d.SQL.QueryRow("SELECT state FROM commands WHERE id=?", cmd.CommandId).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "COMPLETED" {
		t.Fatalf("worker control state=%s", state)
	}

	approval := &pb.Command{
		CommandId: "control-command-approval", Kind: "control",
		Control: &pb.ControlCommand{
			PrincipalId: "owner", OperationId: "operation-approval",
			TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: a.Generation,
			SessionRef: "native-session-1", Action: "approval", ApprovalId: "approval-1",
			RequestVersion: 1, Decision: "accept",
		},
	}
	for i := 0; i < 2; i++ {
		ack, e := w.executeControl(context.Background(), approval)
		if e != nil || ack.State != "COMPLETED" {
			t.Fatalf("approval replay %d ack=%+v err=%v", i, ack, e)
		}
	}
	provider.mu.Lock()
	approvals := provider.approvals
	provider.mu.Unlock()
	if approvals != 1 {
		t.Fatalf("approval side effects=%d want=1", approvals)
	}

	if _, err = d.SQL.Exec(`INSERT INTO workspaces(
		attempt,task,generation,repository_ref,base_commit,path,state,created,updated
	) VALUES(?,?,?,?,?,?,?,?,?)`, a.AttemptId, a.TaskId, a.Generation, "repo", "base", t.TempDir(), "IN_USE", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = d.SQL.Exec(`UPDATE runs SET runtime_state='EXITED',environment_provider='process',
		environment_ref=?,environment_state='ACTIVE',environment_cleanup='PENDING' WHERE id=?`,
		[]byte(`{"provider":"process","id":"attempt-control"}`), a.AttemptId); err != nil {
		t.Fatal(err)
	}
	resume := &pb.Command{
		CommandId: "control-command-resume", Kind: "control",
		Control: &pb.ControlCommand{
			PrincipalId: "owner", OperationId: "operation-resume",
			TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: a.Generation,
			SessionRef: "native-session-1", Action: "resume",
		},
	}
	for i := 0; i < 2; i++ {
		ack, e := w.executeControl(context.Background(), resume)
		if e != nil || ack.State != "COMPLETED" {
			t.Fatalf("resume replay %d ack=%+v err=%v", i, ack, e)
		}
	}
	provider.mu.Lock()
	resumes := provider.resumes
	provider.mu.Unlock()
	if resumes != 1 {
		t.Fatalf("resume side effects=%d want=1", resumes)
	}
	var resumedState string
	if err = d.SQL.QueryRow("SELECT runtime_state FROM runs WHERE id=?", a.AttemptId).Scan(&resumedState); err != nil {
		t.Fatal(err)
	}
	if resumedState != string(adapter.RuntimeRunning) {
		t.Fatalf("runtime state after resume=%s", resumedState)
	}
}

func TestAgentControlWorkerFailsClosedForUncertifiedRuntime(t *testing.T) {
	d, err := store.Open(t.TempDir(), store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	w := &Worker{db: d}
	a := &pb.Assignment{
		TaskId: "task-control", AttemptId: "attempt-control", Generation: 1, LeaseToken: "lease",
		Spec: &pb.TaskSpec{RuntimeProfile: "codex_exec"},
	}
	ref := adapter.ExecutionRef{Provider: "codex_exec", Transport: "local_cli", ID: "runtime-1", PID: 999999, StartID: "missing"}
	rawRef, err := adapter.EncodeExecutionRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.SQL.Exec("INSERT INTO runs(id,assignment,state,runtime_provider,runtime_transport,runtime_ref,runtime_state,runtime_cleanup) VALUES(?,?,?,?,?,?,?,?)",
		a.AttemptId, enc(a), "RUNNING", "codex_exec", "local_cli", rawRef, string(adapter.RuntimeRunning), string(adapter.CleanupPending)); err != nil {
		t.Fatal(err)
	}
	cmd := &pb.Command{
		CommandId: "control-command-unsupported", Kind: "control",
		Control: &pb.ControlCommand{
			PrincipalId: "owner", OperationId: "operation-unsupported",
			TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: a.Generation,
			Action: "input", Mode: "interactive", Input: &pb.Input{Text: "continue"},
		},
	}
	ack, err := w.executeControl(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if ack.State != "REJECTED" || ack.ErrorCode != control.ErrorCapabilityUnsupported.String() {
		t.Fatalf("ack=%+v", ack)
	}
}
