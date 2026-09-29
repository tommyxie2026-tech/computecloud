package worker

import (
	"context"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestControlApprovalWorkerUnsupportedMatrix(t *testing.T) {
	for _, profile := range []string{"codex_exec", "claude_print"} {
		t.Run(profile, func(t *testing.T) {
			d, err := store.Open(t.TempDir(), store.WorkerSchema)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			w := &Worker{db: d}
			a := &pb.Assignment{TaskId: "task", AttemptId: "attempt", Generation: 1, Spec: &pb.TaskSpec{RuntimeProfile: profile}}
			ref, err := adapter.EncodeExecutionRef(adapter.ExecutionRef{Provider: profile, Transport: "local_cli", ID: "fixture", PID: 999999, StartID: "missing"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.SQL.Exec(`INSERT INTO runs(id,assignment,state,runtime_provider,runtime_transport,runtime_ref,runtime_state,runtime_cleanup)
				VALUES(?,?,?,?,?,?,?,?)`, a.AttemptId, enc(a), "RUNNING", profile, "local_cli", ref, string(adapter.RuntimeRunning), string(adapter.CleanupPending)); err != nil {
				t.Fatal(err)
			}
			cmd := &pb.Command{CommandId: "approval-command", Kind: "control", Control: &pb.ControlCommand{
				PrincipalId: "owner", OperationId: "operation", TaskId: a.TaskId, AttemptId: a.AttemptId, Generation: 1,
				Action: "approval", ApprovalId: "approval", RequestVersion: 1, Decision: "ACCEPT",
			}}
			for i := 0; i < 2; i++ {
				ack, err := w.executeControl(context.Background(), cmd)
				if err != nil || ack.State != "REJECTED" || ack.ErrorCode != control.ErrorCapabilityUnsupported.String() {
					t.Fatalf("unsupported ACK=%+v err=%v", ack, err)
				}
			}
		})
	}
}

func TestControlApprovalWorkerUnknownRestartFailsClosed(t *testing.T) {
	dir := t.TempDir()
	d, err := store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	cmd := &pb.Command{CommandId: "approval-unknown", Kind: "control", Control: &pb.ControlCommand{
		PrincipalId: "owner", OperationId: "operation", TaskId: "task", AttemptId: "attempt", Generation: 1,
		Action: "approval", ApprovalId: "approval", RequestVersion: 1, Decision: "ACCEPT",
	}}
	// Simulate a crash after execution reservation, before the outcome can be
	// persisted. There is deliberately no live Runtime to reconstruct or retry.
	if _, err = d.SQL.Exec("INSERT INTO commands(id,hash,state,attempt,operation_id,updated) VALUES(?,?,?,?,?,?)",
		cmd.CommandId, store.Hash(enc(cmd)), "EXECUTING", "attempt", "operation", store.Now()); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = store.Open(dir, store.WorkerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	w := &Worker{db: d}
	if err = w.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ack, err := w.executeControl(context.Background(), cmd)
		if err != nil || ack.State != "UNKNOWN" || ack.ErrorCode != control.ErrorExecutionUnverifiable.String() {
			t.Fatalf("restart replay=%+v err=%v", ack, err)
		}
	}
}
