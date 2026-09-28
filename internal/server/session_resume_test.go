package server

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestAgentControlPersistsLiveSessionRefAndDispatchesResume(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)

	started := &pb.Event{
		TaskId: taskID,
		AttemptId: "control-attempt-1",
		Generation: 1,
		Type: "session.started",
		PayloadJson: config.JSON(map[string]any{"session_ref": "native-session-1"}),
	}
	if err := h.s.db.Tx(context.Background(), func(q store.Query) error {
		return persistRuntimeSessionRef(context.Background(), q, started)
	}); err != nil {
		t.Fatal(err)
	}
	var native string
	if err := h.s.db.SQL.QueryRow("SELECT native_session FROM tasks WHERE id=?", taskID).Scan(&native); err != nil {
		t.Fatal(err)
	}
	if native != "native-session-1" {
		t.Fatalf("native session=%q", native)
	}

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
				Capabilities: []string{"control:session_resume"},
			}},
		},
		identity: config.Identity{WorkerID: "fixture-worker"},
		frames: make(chan *pb.ServerFrame, 1),
		cancel: func(){},
	}
	h.s.mu.Unlock()

	payload, _ := json.Marshal(controlDispatchPayload{SessionRef: "native-session-1"})
	req := ControlOperationRequest{
		OperationID: "resume-op-1",
		OperationType: "resume",
		ResourceType: "session",
		ResourceID: "control-attempt-1",
		TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1",
		ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
		Payload: payload,
	}
	receipt, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, req)
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
	if cmd.Control == nil || cmd.Control.Action != "resume" || cmd.Control.SessionRef != "native-session-1" {
		t.Fatalf("resume command=%+v", cmd.Control)
	}
	if err = h.s.applyControlAck(context.Background(), "fixture-worker", &pb.CommandAck{
		CommandId: commandID, State: "COMPLETED", OperationId: req.OperationID,
	}); err != nil {
		t.Fatal(err)
	}
	var resumed int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM job_events WHERE job_id=? AND type='session.resumed'", jobID).
		Scan(&resumed); err != nil {
		t.Fatal(err)
	}
	if resumed != 1 {
		t.Fatalf("session.resumed events=%d", resumed)
	}
}

func TestAgentControlResumeRejectsSessionRefMismatchAndMissingCapability(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	if _, err := h.s.db.SQL.Exec("UPDATE tasks SET native_session='native-current' WHERE id=?", taskID); err != nil {
		t.Fatal(err)
	}
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
			Runtimes: []*pb.Runtime{{Profile: spec.RuntimeProfile, Version: "fixture"}},
		},
		identity: config.Identity{WorkerID: "fixture-worker"},
		frames: make(chan *pb.ServerFrame, 1),
		cancel: func(){},
	}
	h.s.mu.Unlock()
	makeReq := func(op, ref string) ControlOperationRequest {
		payload, _ := json.Marshal(controlDispatchPayload{SessionRef: ref})
		return ControlOperationRequest{
			OperationID: op, OperationType: "resume",
			ResourceType: "session", ResourceID: "control-attempt-1",
			TaskID: taskID, ExpectedAttemptID: "control-attempt-1",
			ExpectedGeneration: 1, ExpectedResourceVersion: version,
			Payload: payload,
		}
	}
	if _, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, makeReq("resume-no-cap", "native-current")); err == nil {
		t.Fatal("resume accepted without certified runtime capability")
	}

	h.s.mu.Lock()
	h.s.peers["fixture-worker"].hello.Runtimes[0].Capabilities = []string{"control:session_resume"}
	h.s.mu.Unlock()
	if _, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, makeReq("resume-wrong-ref", "native-stale")); err == nil {
		t.Fatal("resume accepted stale native session ref")
	}
}

func TestRuntimeSessionRefRejectsStaleAttempt(t *testing.T) {
	h, _, taskID, _ := controlHarness(t)
	ev := &pb.Event{
		TaskId: taskID, AttemptId: "old-attempt", Generation: 1,
		Type: "session.started",
		PayloadJson: config.JSON(map[string]any{"session_ref": "stale-session"}),
	}
	if err := h.s.db.Tx(context.Background(), func(q store.Query) error {
		return persistRuntimeSessionRef(context.Background(), q, ev)
	}); err == nil {
		t.Fatal("stale Attempt session_ref was accepted")
	}
	var native string
	if err := h.s.db.SQL.QueryRow("SELECT native_session FROM tasks WHERE id=?", taskID).Scan(&native); err != nil {
		t.Fatal(err)
	}
	if native != "" {
		t.Fatalf("stale session mutated task: %q", native)
	}
}

var _ = control.CapabilitySessionResume
