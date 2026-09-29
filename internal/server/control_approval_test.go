package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This server has no background controller so the test can close and reopen
// its real SQLite database at precise delivery boundaries.
func approvalControlHarness(t *testing.T) (*jobHarness, ControlOperationRequest, string) {
	t.Helper()
	base := newJobHarness(t, false, func(c *config.Server) {
		c.Users[0].Scopes = append(c.Users[0].Scopes, "jobs:control")
	})
	cfg := base.s.cfg
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &jobHarness{s: s, ctx: base.ctx, token: base.token}
	t.Cleanup(func() { h.s.Close() })
	j, err := s.SubmitJob(h.ctx, "approval-job", job.JSON(base.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	var task string
	var version int64
	if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT version FROM jobs WHERE id=?", j.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec(`INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released,last_renewed)
		VALUES('approval-attempt',?,'fixture-worker','epoch',1,'token',?,0,?)`, task, store.Now()+60000, store.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec("UPDATE tasks SET attempt='approval-attempt',worker='fixture-worker',current_generation=1,state='RUNNING' WHERE id=?", task); err != nil {
		t.Fatal(err)
	}
	s.peers["fixture-worker"] = &session{hello: &pb.WorkerHello{
		WorkerId: "fixture-worker", Runtimes: []*pb.Runtime{{Profile: "codex_exec", Capabilities: []string{"control:approval"}}},
	}}
	ev := &pb.Event{TaskId: task, AttemptId: "approval-attempt", Generation: 1, Type: "approval.requested",
		PayloadJson: config.JSON(runtimeApprovalPayload{ApprovalID: "approval", Tool: "shell", Action: "run", RiskClass: "HIGH", RequestVersion: 1}),
	}
	if err = s.db.Tx(h.ctx, func(q store.Query) error { return persistApprovalRequested(h.ctx, q, ev) }); err != nil {
		t.Fatal(err)
	}
	return h, ControlOperationRequest{
		OperationID: "approval-op", OperationType: "approval", ResourceType: "approval", ResourceID: "approval",
		TaskID: task, ExpectedAttemptID: "approval-attempt", ExpectedGeneration: 1, ExpectedResourceVersion: version,
		Payload: config.JSON(approvalDecisionPayload{ApprovalID: "approval", RequestVersion: 1, Decision: "ACCEPT"}),
	}, j.ID
}

func dispatchApproval(t *testing.T, h *jobHarness, in ControlOperationRequest, jobID string) *pb.CommandAck {
	t.Helper()
	receipt, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil || receipt.State != "DISPATCHED" {
		t.Fatalf("dispatch receipt=%+v err=%v", receipt, err)
	}
	var id string
	if err = h.s.db.SQL.QueryRow("SELECT id FROM commands WHERE kind='control'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return &pb.CommandAck{CommandId: id, OperationId: in.OperationID, State: "COMPLETED"}
}

func approvalSnapshot(t *testing.T, s *Server) string {
	t.Helper()
	var state, decision, actor string
	var decided int64
	var receipt []byte
	var acked, events int
	if err := s.db.SQL.QueryRow("SELECT state,decision,decided_by,decided_at FROM approval_requests").Scan(&state, &decision, &actor, &decided); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT receipt_json FROM control_operations").Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT acked FROM commands WHERE kind='control'").Scan(&acked); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM job_events WHERE type LIKE 'control.%'").Scan(&events); err != nil {
		t.Fatal(err)
	}
	return string(config.JSON([]any{state, decision, actor, decided, string(receipt), acked, events}))
}

func TestControlApprovalAckReplayAfterRestart(t *testing.T) {
	h, in, jobID := approvalControlHarness(t)
	ack := dispatchApproval(t, h, in, jobID)
	// Dispatch is durable even if the HTTP response was lost and Server stops
	// before receiving the first Worker ACK.
	restart := func() {
		t.Helper()
		cfg := h.s.cfg
		if err := h.s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		h.s = s
	}
	restart()
	if err := h.s.applyControlAck(h.ctx, "fixture-worker", ack); err != nil {
		t.Fatal(err)
	}
	before := approvalSnapshot(t, h.s)
	restart()
	if err := h.s.applyControlAck(h.ctx, "fixture-worker", ack); err != nil {
		t.Fatal(err)
	}
	if after := approvalSnapshot(t, h.s); after != before {
		t.Fatalf("duplicate ACK changed durable state: %s -> %s", before, after)
	}
	replay, err := h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
	if err != nil || !replay.Existing || replay.State != "COMPLETED" {
		t.Fatalf("offline retry=%+v err=%v", replay, err)
	}
	items, err := h.s.JobApprovals(h.ctx, jobID)
	if err != nil || len(items) != 1 || items[0].State != control.ApprovalAccepted {
		t.Fatalf("projection=%+v err=%v", items, err)
	}
	var commands int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM commands WHERE kind='control'").Scan(&commands); err != nil || commands != 1 {
		t.Fatalf("commands=%d err=%v", commands, err)
	}
}

func TestControlApprovalAckConcurrentDuplicateAndConflict(t *testing.T) {
	h, in, jobID := approvalControlHarness(t)
	ack := dispatchApproval(t, h, in, jobID)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- h.s.applyControlAck(context.Background(), "fixture-worker", ack) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	before := approvalSnapshot(t, h.s)
	conflict := &pb.CommandAck{CommandId: ack.CommandId, OperationId: ack.OperationId,
		State: "REJECTED", ErrorCode: "RUNTIME_CONTROL_FAILED"}
	err := h.s.applyControlAck(h.ctx, "fixture-worker", conflict)
	if status.Code(err) != codes.AlreadyExists || status.Convert(err).Message() != control.ErrorOperationConflict.String() {
		t.Fatalf("conflict=%v", err)
	}
	if approvalSnapshot(t, h.s) != before {
		t.Fatal("conflicting ACK mutated terminal truth")
	}
	var events int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM job_events WHERE type='control.completed'").Scan(&events); err != nil || events != 1 {
		t.Fatalf("completion events=%d err=%v", events, err)
	}
}

func TestControlApprovalAckStaleGeneration(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE tasks SET current_generation=2",
		"UPDATE attempts SET released=1",
	} {
		t.Run(mutation, func(t *testing.T) {
			h, in, jobID := approvalControlHarness(t)
			ack := dispatchApproval(t, h, in, jobID)
			before := approvalSnapshot(t, h.s)
			if _, err := h.s.db.SQL.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			err := h.s.applyControlAck(h.ctx, "fixture-worker", ack)
			if status.Code(err) != codes.Aborted || status.Convert(err).Message() != control.ErrorAttemptFenced.String() {
				t.Fatalf("stale ACK=%v", err)
			}
			if approvalSnapshot(t, h.s) != before {
				t.Fatal("stale ACK mutated durable approval/receipt/event")
			}
		})
	}
}

func TestControlApprovalRejectedAckIsImmutable(t *testing.T) {
	for _, state := range []string{"REJECTED", "UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			h, in, jobID := approvalControlHarness(t)
			ack := dispatchApproval(t, h, in, jobID)
			ack.State = state
			if err := h.s.applyControlAck(h.ctx, "fixture-worker", ack); err != nil {
				t.Fatal(err)
			}
			before := approvalSnapshot(t, h.s)
			if err := h.s.applyControlAck(h.ctx, "fixture-worker", ack); err != nil {
				t.Fatal(err)
			}
			ack.State = "COMPLETED"
			if err := h.s.applyControlAck(h.ctx, "fixture-worker", ack); status.Code(err) != codes.AlreadyExists {
				t.Fatalf("late success=%v", err)
			}
			if approvalSnapshot(t, h.s) != before {
				t.Fatal("terminal failure changed after replay/conflict")
			}
		})
	}
}

func TestControlApprovalHTTPProjectionAndDecision(t *testing.T) {
	h, in, jobID := approvalControlHarness(t)
	hs := httptest.NewServer(h.s.HTTPHandler())
	defer hs.Close()
	h.url, h.http = hs.URL, hs.Client()
	code, body := h.request(t, "GET", "/v1/jobs/"+jobID+"/approvals", "", nil)
	var projection struct {
		Approvals []control.ApprovalRequest `json:"approvals"`
	}
	if err := json.Unmarshal(body, &projection); err != nil || code != 200 || len(projection.Approvals) != 1 || projection.Approvals[0].State != control.ApprovalPending {
		t.Fatalf("projection status=%d body=%s err=%v", code, body, err)
	}
	decision := config.JSON(map[string]any{
		"operation_id": in.OperationID, "task_id": in.TaskID, "expected_attempt_id": in.ExpectedAttemptID,
		"expected_generation": in.ExpectedGeneration, "expected_resource_version": in.ExpectedResourceVersion,
		"request_version": 1, "decision": "ACCEPT",
	})
	path := "/v1/jobs/" + jobID + "/approvals/approval"
	code, body = h.request(t, "POST", path, "", decision)
	if code != 202 {
		t.Fatalf("decision status=%d body=%s", code, body)
	}
	code, body = h.request(t, "POST", path, "", decision)
	var receipt ControlOperationReceipt
	if err := json.Unmarshal(body, &receipt); err != nil || code != 200 || !receipt.Existing || receipt.State != "DISPATCHED" {
		t.Fatalf("retry status=%d body=%s err=%v", code, body, err)
	}
}

func TestControlApprovalUnsupportedRuntimeMatrix(t *testing.T) {
	for _, profile := range []string{"codex_exec", "claude_print"} {
		t.Run(profile, func(t *testing.T) {
			h, in, jobID := approvalControlHarness(t)
			provider, ok := adapter.Lookup(profile)
			if !ok {
				t.Fatal("missing builtin provider")
			}
			desc, claimed, err := adapter.ControlDescriptorFor(provider)
			if err != nil || !claimed {
				t.Fatalf("descriptor err=%v claimed=%v", err, claimed)
			}
			var capabilities []string
			for _, capability := range desc.Capabilities {
				capabilities = append(capabilities, "control:"+string(capability))
			}
			h.s.peers["fixture-worker"].hello.Runtimes = []*pb.Runtime{{Profile: profile, Capabilities: capabilities}}
			var raw []byte
			if err = h.s.db.SQL.QueryRow("SELECT spec FROM tasks WHERE id=?", in.TaskID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			spec := new(pb.TaskSpec)
			if err = decode(raw, spec); err != nil {
				t.Fatal(err)
			}
			spec.RuntimeProfile = profile
			if _, err = h.s.db.SQL.Exec("UPDATE tasks SET spec=? WHERE id=?", encode(spec), in.TaskID); err != nil {
				t.Fatal(err)
			}
			_, err = h.s.acceptAndDispatchControlOperation(h.ctx, jobID, in)
			if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != control.ErrorCapabilityUnsupported.String() {
				t.Fatalf("unsupported=%v", err)
			}
			var count int
			if err = h.s.db.SQL.QueryRow("SELECT (SELECT count(*) FROM control_operations)+(SELECT count(*) FROM commands WHERE kind='control')").Scan(&count); err != nil || count != 0 {
				t.Fatalf("unsupported operation persisted=%d err=%v", count, err)
			}
		})
	}
}

func TestControlApprovalDecisionPayloadValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"approval_id":"a","request_version":0,"decision":"ACCEPT"}`, `{"approval_id":"a","request_version":1,"decision":"APPROVE_NEXT_REPLAN"}`} {
		_, _, err := requiredControlCapability(ControlOperationRequest{OperationType: "approval", Payload: json.RawMessage(raw)})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("payload=%s err=%v", raw, err)
		}
	}
}
