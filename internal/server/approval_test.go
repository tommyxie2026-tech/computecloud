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

func TestApprovalLifecycleVersionFencingAndAck(t *testing.T) {
	h, jobID, taskID, jobVersion := controlHarness(t)

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
			Runtimes: []*pb.Runtime{{Profile: spec.RuntimeProfile, Version: "fixture", Capabilities: []string{"control:approval"}}},
		},
		identity: config.Identity{WorkerID: "fixture-worker"},
		frames: make(chan *pb.ServerFrame, 1),
		cancel: func(){},
	}
	h.s.mu.Unlock()

	emit := func(version int64, expires int64) {
		t.Helper()
		payload := map[string]any{
			"approval_id": "approval-1",
			"tool": "shell",
			"action": "run",
			"risk_class": "HIGH",
			"arguments_summary": "git status",
			"request_version": version,
			"expires_at_ms": expires,
		}
		ev := &pb.Event{
			TaskId: taskID, AttemptId: "control-attempt-1", Generation: 1,
			Type: "approval.requested", PayloadJson: config.JSON(payload),
		}
		if err := h.s.db.Tx(context.Background(), func(q store.Query) error {
			return persistApprovalRequested(context.Background(), q, ev)
		}); err != nil {
			t.Fatal(err)
		}
	}

	emit(1, store.Now()+60000)
	emit(2, store.Now()+60000)

	var v1, v2 string
	if err := h.s.db.SQL.QueryRow("SELECT state FROM approval_requests WHERE approval_id='approval-1' AND request_version=1").Scan(&v1); err != nil {
		t.Fatal(err)
	}
	if err := h.s.db.SQL.QueryRow("SELECT state FROM approval_requests WHERE approval_id='approval-1' AND request_version=2").Scan(&v2); err != nil {
		t.Fatal(err)
	}
	if v1 != "SUPERSEDED" || v2 != "PENDING" {
		t.Fatalf("states v1=%s v2=%s", v1, v2)
	}

	oldPayload := approvalDecisionPayload{ApprovalID: "approval-1", RequestVersion: 1, Decision: "ACCEPT"}
	oldReq := ControlOperationRequest{
		OperationID: "approval-old", OperationType: "approval",
		ResourceType: "approval", ResourceID: "approval-1",
		TaskID: taskID, ExpectedAttemptID: "control-attempt-1", ExpectedGeneration: 1,
		ExpectedResourceVersion: jobVersion, Payload: json.RawMessage(config.JSON(oldPayload)),
	}
	if _, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, oldReq); err == nil {
		t.Fatal("superseded approval decision accepted")
	}

	payload := approvalDecisionPayload{ApprovalID: "approval-1", RequestVersion: 2, Decision: "ACCEPT"}
	req := ControlOperationRequest{
		OperationID: "approval-current", OperationType: "approval",
		ResourceType: "approval", ResourceID: "approval-1",
		TaskID: taskID, ExpectedAttemptID: "control-attempt-1", ExpectedGeneration: 1,
		ExpectedResourceVersion: jobVersion, Payload: json.RawMessage(config.JSON(payload)),
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
	if err = h.s.db.SQL.QueryRow("SELECT id,body FROM commands WHERE kind='control' AND task=? ORDER BY id DESC LIMIT 1", taskID).
		Scan(&commandID, &body); err != nil {
		t.Fatal(err)
	}
	cmd := new(pb.Command)
	if err = decode(body, cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Control == nil || cmd.Control.Action != "approval" || cmd.Control.ApprovalId != "approval-1" ||
		cmd.Control.RequestVersion != 2 || cmd.Control.Decision != "ACCEPT" {
		t.Fatalf("command=%+v", cmd.Control)
	}

	if err = h.s.applyControlAck(context.Background(), "fixture-worker", &pb.CommandAck{
		CommandId: commandID, State: "COMPLETED", OperationId: req.OperationID,
	}); err != nil {
		t.Fatal(err)
	}
	if err = h.s.db.SQL.QueryRow("SELECT state FROM approval_requests WHERE approval_id='approval-1' AND request_version=2").Scan(&v2); err != nil {
		t.Fatal(err)
	}
	if v2 != "ACCEPTED" {
		t.Fatalf("approval state=%s", v2)
	}
}

func TestApprovalExpiryAndAttemptFencing(t *testing.T) {
	h, jobID, taskID, jobVersion := controlHarness(t)
	ev := &pb.Event{
		TaskId: taskID, AttemptId: "control-attempt-1", Generation: 1,
		Type: "approval.requested",
		PayloadJson: config.JSON(map[string]any{
			"approval_id": "approval-expired",
			"tool": "shell",
			"action": "run",
			"risk_class": "MEDIUM",
			"request_version": 1,
			"requested_at_ms": store.Now()-1000,
			"expires_at_ms": store.Now()-1,
		}),
	}
	if err := h.s.db.Tx(context.Background(), func(q store.Query) error {
		return persistApprovalRequested(context.Background(), q, ev)
	}); err != nil {
		t.Fatal(err)
	}
	items, err := h.s.JobApprovals(h.ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	foundExpired := false
	for _, item := range items {
		if item.ApprovalID == "approval-expired" && item.State == control.ApprovalExpired {
			foundExpired = true
		}
	}
	if !foundExpired {
		t.Fatal("expired approval not projected")
	}

	payload := approvalDecisionPayload{ApprovalID: "approval-expired", RequestVersion: 1, Decision: "REJECT"}
	req := ControlOperationRequest{
		OperationID: "approval-expired-op", OperationType: "approval",
		ResourceType: "approval", ResourceID: "approval-expired",
		TaskID: taskID, ExpectedAttemptID: "stale-attempt", ExpectedGeneration: 1,
		ExpectedResourceVersion: jobVersion, Payload: json.RawMessage(config.JSON(payload)),
	}
	if _, err = h.s.acceptAndDispatchControlOperation(h.ctx, jobID, req); err == nil {
		t.Fatal("expired/stale approval accepted")
	}
}
