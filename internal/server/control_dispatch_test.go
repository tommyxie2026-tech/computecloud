package server

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestAgentControlDispatchAndFinalAck(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	var specRaw []byte
	if err := h.s.db.SQL.QueryRow("SELECT spec FROM tasks WHERE id=?", taskID).Scan(&specRaw); err != nil {
		t.Fatal(err)
	}
	spec := new(pb.TaskSpec)
	if err := decode(specRaw, spec); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	h.s.peers["fixture-worker"] = &session{
		hello: &pb.WorkerHello{
			WorkerId: "fixture-worker", Epoch: "epoch-1", Slots: 1,
			Runtimes: []*pb.Runtime{{
				Profile: spec.RuntimeProfile,
				Version: "fixture",
				Capabilities: []string{"control:queue_next_input", "control:interrupt"},
			}},
		},
		identity: config.Identity{WorkerID: "fixture-worker"},
		frames: make(chan *pb.ServerFrame, 1),
		cancel: func(){},
	}
	h.s.mu.Unlock()

	in := ControlOperationRequest{
		OperationID: "control-dispatch-1",
		OperationType: "input",
		ResourceType: "session",
		ResourceID: "control-attempt-1",
		TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1",
		ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
		Payload: json.RawMessage(`{"mode":"queue_next","content":"continue"}`),
	}
	receipt, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "DISPATCHED" {
		t.Fatalf("receipt=%+v", receipt)
	}
	var commandID string
	var body []byte
	if err = h.s.db.SQL.QueryRow("SELECT id,body FROM commands WHERE kind='control' AND task=?", taskID).
		Scan(&commandID, &body); err != nil {
		t.Fatal(err)
	}
	cmd := new(pb.Command)
	if err = decode(body, cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Control == nil || cmd.Control.OperationId != in.OperationID || cmd.Control.PrincipalId == "" ||
		cmd.Control.Action != "input" || cmd.Control.Mode != "queue_next" || cmd.Control.GetInput().GetText() != "continue" {
		t.Fatalf("control command=%+v", cmd.Control)
	}

	if err = h.s.applyControlAck(context.Background(), "fixture-worker", &pb.CommandAck{
		CommandId: commandID, State: "COMPLETED", OperationId: in.OperationID,
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	var rawReceipt []byte
	if err = h.s.db.SQL.QueryRow("SELECT state,receipt_json FROM control_operations WHERE operation_id=?", in.OperationID).
		Scan(&state, &rawReceipt); err != nil {
		t.Fatal(err)
	}
	if state != "COMPLETED" {
		t.Fatalf("operation state=%s", state)
	}
	var final ControlOperationReceipt
	if err = json.Unmarshal(rawReceipt, &final); err != nil {
		t.Fatal(err)
	}
	if final.State != "COMPLETED" || final.ErrorCode != "" {
		t.Fatalf("final receipt=%+v", final)
	}
	var completed int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM job_events WHERE job_id=? AND type='control.completed'", jobID).
		Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatalf("control.completed events=%d", completed)
	}

	h.s.mu.Lock()
	delete(h.s.peers, "fixture-worker")
	h.s.mu.Unlock()
	replay, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Existing || replay.State != "COMPLETED" {
		t.Fatalf("offline replay receipt=%+v", replay)
	}
	var commands int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM commands WHERE kind='control' AND task=?", taskID).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if commands != 1 {
		t.Fatalf("duplicate control commands=%d", commands)
	}
}

func TestAgentControlUnknownAckBecomesUnverifiableRejection(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	var specRaw []byte
	if err := h.s.db.SQL.QueryRow("SELECT spec FROM tasks WHERE id=?", taskID).Scan(&specRaw); err != nil {
		t.Fatal(err)
	}
	spec := new(pb.TaskSpec)
	if err := decode(specRaw, spec); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	h.s.peers["fixture-worker"] = &session{
		hello: &pb.WorkerHello{
			WorkerId: "fixture-worker", Epoch: "epoch-1", Slots: 1,
			Runtimes: []*pb.Runtime{{Profile: spec.RuntimeProfile, Version: "fixture", Capabilities: []string{"control:interrupt"}}},
		},
		identity: config.Identity{WorkerID: "fixture-worker"},
		frames: make(chan *pb.ServerFrame, 1),
		cancel: func(){},
	}
	h.s.mu.Unlock()
	in := ControlOperationRequest{
		OperationID: "control-unknown-1", OperationType: "interrupt",
		ResourceType: "session", ResourceID: "control-attempt-1", TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1", ExpectedGeneration: 1, ExpectedResourceVersion: version,
	}
	if _, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in); err != nil {
		t.Fatal(err)
	}
	var commandID string
	if err := h.s.db.SQL.QueryRow("SELECT id FROM commands WHERE kind='control' AND task=?", taskID).Scan(&commandID); err != nil {
		t.Fatal(err)
	}
	if err := h.s.applyControlAck(context.Background(), "fixture-worker", &pb.CommandAck{
		CommandId: commandID, State: "UNKNOWN", OperationId: in.OperationID,
	}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := h.s.db.SQL.QueryRow("SELECT receipt_json FROM control_operations WHERE operation_id=?", in.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var receipt ControlOperationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.State != "REJECTED" || receipt.ErrorCode != control.ErrorExecutionUnverifiable.String() {
		t.Fatalf("receipt=%+v", receipt)
	}
}

// Keep imports anchored to the same fixture helpers used by ACP-3a.
var _ = job.Hash
var _ = store.Now


func TestAgentControlDispatchResume(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	var specRaw []byte
	if err := h.s.db.SQL.QueryRow("SELECT spec FROM tasks WHERE id=?", taskID).Scan(&specRaw); err != nil {
		t.Fatal(err)
	}
	spec := new(pb.TaskSpec)
	if err := decode(specRaw, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.db.SQL.Exec("UPDATE tasks SET native_session=? WHERE id=?", "native-session-1", taskID); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	h.s.peers["fixture-worker"] = &session{
		hello: &pb.WorkerHello{
			WorkerId: "fixture-worker", Epoch: "epoch-1", Slots: 1,
			Runtimes: []*pb.Runtime{{
				Profile: spec.RuntimeProfile, Version: "fixture",
				Capabilities: []string{"control:session_resume"},
			}},
		},
		identity: config.Identity{WorkerID: "fixture-worker"},
		frames: make(chan *pb.ServerFrame, 1),
		cancel: func(){},
	}
	h.s.mu.Unlock()

	in := ControlOperationRequest{
		OperationID: "resume-dispatch-1", OperationType: "resume",
		ResourceType: "session", ResourceID: "control-attempt-1", TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1", ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
	}
	receipt, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "DISPATCHED" {
		t.Fatalf("receipt=%+v", receipt)
	}
	var body []byte
	if err = h.s.db.SQL.QueryRow("SELECT body FROM commands WHERE kind='control' AND task=?", taskID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	cmd := new(pb.Command)
	if err = decode(body, cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Control == nil || cmd.Control.Action != "resume" || cmd.Control.SessionRef != "native-session-1" {
		t.Fatalf("resume command=%+v", cmd.Control)
	}
}
