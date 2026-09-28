package server

import (
	"encoding/json"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func controlHarness(t *testing.T) (*jobHarness, string, string, int64) {
	t.Helper()
	h := newJobHarness(t, false, func(cfg *config.Server) {
		cfg.Users[0].Scopes = append(cfg.Users[0].Scopes, "jobs:control")
	})
	j, err := h.s.SubmitJob(h.ctx, "control-ledger-job", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err = h.s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	const attemptID = "control-attempt-1"
	if _, err = h.s.db.SQL.Exec(`INSERT INTO attempts(
		id,task,worker,epoch,generation,token,lease_until,released,last_renewed
	) VALUES(?,?,?,?,?,?,?,?,?)`,
		attemptID, taskID, "fixture-worker", "epoch-1", 1, "token-1",
		store.Now()+60000, 0, store.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.db.SQL.Exec(`UPDATE tasks
		SET attempt=?,worker=?,current_generation=?,state='RUNNING'
		WHERE id=?`, attemptID, "fixture-worker", 1, taskID); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = h.s.db.SQL.QueryRow("SELECT version FROM jobs WHERE id=?", j.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return h, j.ID, taskID, version
}

func TestAgentControlOperationIdempotencyAndFencing(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	base := ControlOperationRequest{
		OperationID: "control-op-1",
		OperationType: "cancel",
		ResourceType: "session",
		ResourceID: "control-attempt-1",
		TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1",
		ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
		Payload: json.RawMessage(`{"reason":"user requested"}`),
	}

	first, err := h.s.acceptControlOperation(h.ctx, jobID, base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Existing || first.State != "ACCEPTED" {
		t.Fatalf("first receipt=%+v", first)
	}

	replay, err := h.s.acceptControlOperation(h.ctx, jobID, base)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Existing || replay.OperationID != first.OperationID {
		t.Fatalf("replay receipt=%+v", replay)
	}

	conflict := base
	conflict.Payload = json.RawMessage(`{"reason":"different"}`)
	if _, err = h.s.acceptControlOperation(h.ctx, jobID, conflict); status.Code(err) != codes.AlreadyExists || status.Convert(err).Message() != "OPERATION_CONFLICT" {
		t.Fatalf("conflict err=%v", err)
	}

	stale := base
	stale.OperationID = "control-op-stale"
	stale.ExpectedGeneration = 2
	if _, err = h.s.acceptControlOperation(h.ctx, jobID, stale); status.Code(err) != codes.Aborted || status.Convert(err).Message() != "ATTEMPT_FENCED" {
		t.Fatalf("stale generation err=%v", err)
	}

	staleAttempt := base
	staleAttempt.OperationID = "control-op-old-attempt"
	staleAttempt.ExpectedAttemptID = "old-attempt"
	if _, err = h.s.acceptControlOperation(h.ctx, jobID, staleAttempt); status.Code(err) != codes.Aborted || status.Convert(err).Message() != "ATTEMPT_FENCED" {
		t.Fatalf("stale attempt err=%v", err)
	}

	staleVersion := base
	staleVersion.OperationID = "control-op-stale-version"
	staleVersion.ExpectedResourceVersion = version + 1
	if _, err = h.s.acceptControlOperation(h.ctx, jobID, staleVersion); status.Code(err) != codes.Aborted || status.Convert(err).Message() != "RESOURCE_VERSION_CONFLICT" {
		t.Fatalf("stale resource version err=%v", err)
	}

	var count int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM control_operations WHERE job_id=?", jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable operations=%d want=1", count)
	}
}

func TestAgentControlOperationReleasedAttemptFailsClosed(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	if _, err := h.s.db.SQL.Exec("UPDATE attempts SET released=1 WHERE id='control-attempt-1'"); err != nil {
		t.Fatal(err)
	}
	in := ControlOperationRequest{
		OperationID: "control-op-released",
		OperationType: "interrupt",
		ResourceType: "session",
		ResourceID: "control-attempt-1",
		TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1",
		ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
	}
	if _, err := h.s.acceptControlOperation(h.ctx, jobID, in); status.Code(err) != codes.Aborted || status.Convert(err).Message() != "ATTEMPT_FENCED" {
		t.Fatalf("released attempt err=%v", err)
	}
}


func TestAgentControlOperationUnsupportedCapabilityFailsClosed(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	in := ControlOperationRequest{
		OperationID: "control-op-unsupported",
		OperationType: "input",
		ResourceType: "session",
		ResourceID: "control-attempt-1",
		TaskID: taskID,
		ExpectedAttemptID: "control-attempt-1",
		ExpectedGeneration: 1,
		ExpectedResourceVersion: version,
		Payload: json.RawMessage(`{"mode":"queue_next","content":"continue"}`),
	}
	_, err := h.s.acceptControlOperation(h.ctx, jobID, in)
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != control.ErrorCapabilityUnsupported {
		t.Fatalf("unsupported capability err=%v", err)
	}
	var count int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM control_operations WHERE operation_id=?", in.OperationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unsupported operation was persisted: count=%d", count)
	}
}


func TestAgentControlDispatchPersistsStructuredCommand(t *testing.T) {
	h, jobID, taskID, version := controlHarness(t)
	h.s.mu.Lock()
	h.s.peers["fixture-worker"] = &session{hello: &pb.WorkerHello{
		WorkerId: "fixture-worker",
		Epoch: "epoch-1",
		Runtimes: []*pb.Runtime{{
			Profile: "codex_exec",
			Version: "fixture-1",
			Capabilities: []string{
				"runtime:event_stream",
				"control:interactive_input",
			},
		}},
	}}
	h.s.mu.Unlock()

	in := ControlOperationRequest{
		OperationID: "control-op-dispatch",
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

	var raw []byte
	if err = h.s.db.SQL.QueryRow("SELECT body FROM commands WHERE kind='control' AND attempt=?", in.ExpectedAttemptID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	cmd := new(pb.Command)
	if err = decode(raw, cmd); err != nil {
		t.Fatal(err)
	}
	cc := cmd.GetControl()
	if cc == nil || cc.OperationId != in.OperationID || cc.TaskId != taskID ||
		cc.AttemptId != in.ExpectedAttemptID || cc.Generation != in.ExpectedGeneration ||
		cc.LeaseToken != "token-1" || cc.Mode != "queue_next" {
		t.Fatalf("control command=%+v", cc)
	}
	ok, err := commandDeliverable(h.ctx, h.s.db.SQL, cmd, "fixture-worker", "epoch-1")
	if err != nil || !ok {
		t.Fatalf("current control deliverable=%v err=%v", ok, err)
	}
	cc.Generation++
	ok, err = commandDeliverable(h.ctx, h.s.db.SQL, cmd, "fixture-worker", "epoch-1")
	if err != nil || ok {
		t.Fatalf("stale control deliverable=%v err=%v", ok, err)
	}

	// A retry of the same user operation must converge on the same durable
	// command instead of creating a second runtime side effect.
	cc.Generation--
	receipt, err = h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Existing || receipt.State != "DISPATCHED" {
		t.Fatalf("replayed receipt=%+v", receipt)
	}
	var count int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM commands WHERE kind='control' AND attempt=?", in.ExpectedAttemptID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("control commands=%d want=1", count)
	}
}
