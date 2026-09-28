package server

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func attachControlRuntime(t *testing.T, h *jobHarness, taskID string, capabilities ...string) {
	t.Helper()
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
			Runtimes: []*pb.Runtime{{Profile: spec.RuntimeProfile, Version: "fixture", Capabilities: capabilities}},
		},
		identity: config.Identity{WorkerID: "fixture-worker"},
		frames: make(chan *pb.ServerFrame, 4),
		cancel: func(){},
	}
	h.s.mu.Unlock()
}

func TestACP4ApprovalLifecycleAndDecisionFencing(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	attachControlRuntime(t, h, taskID, "control:approval")
	expires := store.Now() + 60000
	ev := &pb.Event{
		TaskId: taskID, AttemptId: "control-attempt-1", Generation: 1,
		Type: "approval.requested",
		PayloadJson: []byte(`{"approval_id":"approval-1","tool":"shell","action":"run tests","risk_class":"HIGH","request_version":1,"expires_at_ms":` + fmtInt(expires) + `}`),
	}
	if err := h.s.db.Tx(context.Background(), func(q store.Query) error {
		return persistAgentControlRuntimeState(context.Background(), q, ev)
	}); err != nil {
		t.Fatal(err)
	}
	items, err := h.s.JobApprovals(h.ctx, jobID)
	if err != nil || len(items) != 1 || items[0].State != "PENDING" {
		t.Fatalf("approvals=%+v err=%v", items, err)
	}

	in := ControlOperationRequest{
		OperationID: "approval-decision-1", OperationType: "approval",
		ResourceType: "approval", ResourceID: "approval-1", TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1", ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
		Payload: json.RawMessage(`{"approval_id":"approval-1","request_version":1,"decision":"accept"}`),
	}
	receipt, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil || receipt.State != "DISPATCHED" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	var commandID string
	var body []byte
	if err = h.s.db.SQL.QueryRow("SELECT id,body FROM commands WHERE kind='control' AND task=?", taskID).Scan(&commandID, &body); err != nil {
		t.Fatal(err)
	}
	cmd := new(pb.Command)
	if err = decode(body, cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Control == nil || cmd.Control.Action != "approval" || cmd.Control.ApprovalId != "approval-1" ||
		cmd.Control.RequestVersion != 1 || cmd.Control.Decision != "accept" {
		t.Fatalf("approval command=%+v", cmd.Control)
	}
	if err = h.s.applyControlAck(context.Background(), "fixture-worker", &pb.CommandAck{
		CommandId: commandID, State: "COMPLETED", OperationId: in.OperationID,
	}); err != nil {
		t.Fatal(err)
	}
	items, err = h.s.JobApprovals(h.ctx, jobID)
	if err != nil || len(items) != 1 || items[0].State != "ACCEPTED" {
		t.Fatalf("final approvals=%+v err=%v", items, err)
	}

	stale := in
	stale.OperationID = "approval-stale-version"
	stale.Payload = json.RawMessage(`{"approval_id":"approval-1","request_version":2,"decision":"accept"}`)
	if _, err = h.s.acceptAndDispatchControlOperation(h.ctx, jobID, stale); status.Code(err) != codes.Aborted ||
		status.Convert(err).Message() != "RESOURCE_VERSION_CONFLICT" {
		t.Fatalf("stale approval version err=%v", err)
	}
}

func TestACP4ApprovalExpiration(t *testing.T) {
	h, jobID, taskID, _ := controlHarness(t)
	ev := &pb.Event{
		TaskId: taskID, AttemptId: "control-attempt-1", Generation: 1,
		Type: "approval.requested",
		PayloadJson: []byte(`{"approval_id":"approval-expire","tool":"shell","action":"run","risk_class":"MEDIUM","request_version":1,"expires_at_ms":` + fmtInt(store.Now()+60000) + `}`),
	}
	if err := h.s.db.Tx(context.Background(), func(q store.Query) error {
		return persistAgentControlRuntimeState(context.Background(), q, ev)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.db.SQL.Exec("UPDATE approval_requests SET expires_at=? WHERE approval_id='approval-expire'", store.Now()-1); err != nil {
		t.Fatal(err)
	}
	items, err := h.s.JobApprovals(h.ctx, jobID)
	if err != nil || len(items) != 1 || items[0].State != "EXPIRED" {
		t.Fatalf("expired approvals=%+v err=%v", items, err)
	}
}

func TestACP4SessionResumeRequiresExplicitCurrentSessionRef(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	attachControlRuntime(t, h, taskID, "control:session_resume")
	if _, err := h.s.db.SQL.Exec("UPDATE attempts SET runtime_session_ref='native-session-1' WHERE id='control-attempt-1'"); err != nil {
		t.Fatal(err)
	}
	in := ControlOperationRequest{
		OperationID: "resume-1", OperationType: "resume",
		ResourceType: "session", ResourceID: "control-attempt-1", TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1", ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
		Payload: json.RawMessage(`{"session_ref":"native-session-1"}`),
	}
	receipt, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil || receipt.State != "DISPATCHED" {
		t.Fatalf("resume receipt=%+v err=%v", receipt, err)
	}
	var commandID string
	var body []byte
	if err = h.s.db.SQL.QueryRow("SELECT id,body FROM commands WHERE kind='control' AND task=?", taskID).Scan(&commandID, &body); err != nil {
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
		CommandId: commandID, State: "COMPLETED", OperationId: in.OperationID,
	}); err != nil {
		t.Fatal(err)
	}
	var resumed int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM job_events WHERE job_id=? AND type='session.resumed'", jobID).Scan(&resumed); err != nil || resumed != 1 {
		t.Fatalf("session.resumed=%d err=%v", resumed, err)
	}

	bad := in
	bad.OperationID = "resume-wrong-ref"
	bad.Payload = json.RawMessage(`{"session_ref":"other"}`)
	if _, err = h.s.acceptAndDispatchControlOperation(h.ctx, jobID, bad); status.Code(err) != codes.Aborted ||
		status.Convert(err).Message() != "RESOURCE_VERSION_CONFLICT" {
		t.Fatalf("wrong session ref err=%v", err)
	}
}
