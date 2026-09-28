package worker

import (
	"context"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type controlRemoteProvider struct {
	recoveryRemoteProvider
	inputs int
}

func (p *controlRemoteProvider) Profile() string { return "control_remote_fixture" }
func (p *controlRemoteProvider) Capabilities() adapter.CapabilitySet {
	return adapter.CapabilitySet{Runtime: []string{"event_stream", "remote_api"}}
}
func (p *controlRemoteProvider) ControlDescriptor() adapter.ControlDescriptor {
	return adapter.ControlDescriptor{
		ProtocolVersion: control.ProtocolV1Alpha1,
		Capabilities: []control.Capability{
			control.CapabilityStreamOutput,
			control.CapabilityStructuredOutput,
			control.CapabilityInteractiveInput,
			control.CapabilityInterrupt,
			control.CapabilityApproval,
			control.CapabilitySessionResume,
		},
	}
}
func (p *controlRemoteProvider) Input(context.Context, adapter.ControlInputRequest) error {
	p.inputs++
	return nil
}
func (p *controlRemoteProvider) Interrupt(context.Context, adapter.ControlInterruptRequest) error {
	return nil
}
func (p *controlRemoteProvider) Approve(context.Context, adapter.ControlApprovalRequest) error {
	return nil
}
func (p *controlRemoteProvider) Resume(context.Context, adapter.ControlResumeRequest) (adapter.ExecutionRef, error) {
	return adapter.ExecutionRef{Provider: p.Profile(), Transport: "remote_api", ID: "resumed"}, nil
}

var _ adapter.SessionControlProvider = (*controlRemoteProvider)(nil)

func TestAgentControlWorkerDispatchIsIdempotent(t *testing.T) {
	provider := &controlRemoteProvider{}
	if existing, ok := adapter.Lookup(provider.Profile()); ok {
		got, cast := existing.(*controlRemoteProvider)
		if !cast {
			t.Fatalf("unexpected existing provider %T", existing)
		}
		provider = got
	} else if err := adapter.Register(provider); err != nil {
		t.Fatal(err)
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
				provider.Profile(): {Version: "fixture-v1"},
			},
		},
	}
	a := &pb.Assignment{
		TaskId: "task-1", AttemptId: "attempt-1", Generation: 3, LeaseToken: "lease-1",
		Spec: &pb.TaskSpec{RuntimeProfile: provider.Profile()},
	}
	ref := adapter.ExecutionRef{Provider: provider.Profile(), Transport: "remote_api", ID: "run-1"}
	refRaw, err := adapter.EncodeExecutionRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec(`INSERT INTO runs(
		id,assignment,state,runtime_provider,runtime_transport,runtime_ref,runtime_state,runtime_cleanup
	) VALUES(?,?,?,?,?,?,?,?)`,
		a.AttemptId, enc(a), "RUNNING", provider.Profile(), "remote_api", refRaw,
		string(adapter.RuntimeRunning), string(adapter.CleanupPending)); err != nil {
		t.Fatal(err)
	}
	cmd := &pb.Command{
		CommandId: "command-1",
		Kind: "control",
		Control: &pb.ControlCommand{
			OperationId: "operation-1",
			OperationType: "input",
			JobId: "job-1",
			TaskId: a.TaskId,
			AttemptId: a.AttemptId,
			Generation: a.Generation,
			SessionRef: "session-1",
			LeaseToken: a.LeaseToken,
			PayloadJson: []byte(`{"mode":"queue_next","content":"continue"}`),
		},
	}
	execute, err := w.acceptControl(context.Background(), cmd)
	if err != nil || !execute {
		t.Fatalf("first accept execute=%v err=%v", execute, err)
	}
	if err = w.handleControl(context.Background(), cmd.Control); err != nil {
		t.Fatal(err)
	}
	execute, err = w.acceptControl(context.Background(), cmd)
	if err != nil || execute {
		t.Fatalf("replay accept execute=%v err=%v", execute, err)
	}
	if provider.inputs != 1 {
		t.Fatalf("input executions=%d want=1", provider.inputs)
	}

	var raw []byte
	if err = db.SQL.QueryRow("SELECT body FROM events WHERE attempt=? AND seq=1", a.AttemptId).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	ev := new(pb.Event)
	if err = dec(raw, ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "control.accepted" {
		t.Fatalf("event type=%q payload=%s", ev.Type, ev.PayloadJson)
	}
}

func TestAgentControlWorkerRejectsStaleIdentity(t *testing.T) {
	db, err := store.Open(t.TempDir(), store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := &Worker{db: db}
	a := &pb.Assignment{
		TaskId: "task-1", AttemptId: "attempt-1", Generation: 2, LeaseToken: "lease-current",
		Spec: &pb.TaskSpec{RuntimeProfile: "control_remote_fixture"},
	}
	if _, err = db.SQL.Exec("INSERT INTO runs(id,assignment,state) VALUES(?,?,?)",
		a.AttemptId, enc(a), "RUNNING"); err != nil {
		t.Fatal(err)
	}
	cmd := &pb.Command{
		CommandId: "stale-command", Kind: "control",
		Control: &pb.ControlCommand{
			OperationId: "operation-stale", OperationType: "input",
			TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: 1,
			LeaseToken: "lease-old", PayloadJson: []byte(`{}`),
		},
	}
	if execute, err := w.acceptControl(context.Background(), cmd); err == nil || execute {
		t.Fatalf("stale control accepted execute=%v err=%v", execute, err)
	}
}
