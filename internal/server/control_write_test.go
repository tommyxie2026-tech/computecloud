package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func waitControlSession(t *testing.T, h *jobHarness, jobID string) control.AgentSession {
	t.Helper()
	for {
		sessions, err := h.s.JobSessions(h.ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) > 0 {
			session := sessions[0]
			switch session.State {
			case control.SessionStarting, control.SessionRunning, control.SessionWaitingInput, control.SessionWaitingApproval:
				return session
			}
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("timed out waiting for control session")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestAgentControlSafeCancelIdempotentAndFenced(t *testing.T) {
	h := newJobHarness(t, true)
	spec := h.spec("single")
	spec.Input.Text = "slow-job"

	j, err := h.s.SubmitJob(h.ctx, "control-safe-cancel", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	session := waitControlSession(t, h, j.ID)

	req := SessionControlRequest{
		OperationID:        "cancel-op-1",
		ExpectedAttemptID:  session.AttemptID,
		ExpectedGeneration: session.Generation,
		Reason:             "user requested",
	}
	raw, _ := json.Marshal(req)
	path := "/v1/jobs/" + j.ID + "/sessions/" + session.SessionID + "/cancel"

	code, body := h.request(t, http.MethodPost, path, "", raw)
	if code != http.StatusAccepted {
		t.Fatalf("first cancel: %d %s", code, body)
	}
	var receipt control.OperationReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.State != control.OperationAccepted || receipt.Existing ||
		receipt.AttemptID != session.AttemptID || receipt.Generation != session.Generation {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}

	code, body = h.request(t, http.MethodPost, path, "", raw)
	if code != http.StatusOK {
		t.Fatalf("replay cancel: %d %s", code, body)
	}
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Existing {
		t.Fatalf("replay was not identified: %+v", receipt)
	}

	conflict := req
	conflict.Reason = "different payload"
	conflictRaw, _ := json.Marshal(conflict)
	code, body = h.request(t, http.MethodPost, path, "", conflictRaw)
	if code != http.StatusConflict || !jsonBodyHasErrorCode(body, "OPERATION_CONFLICT") {
		t.Fatalf("operation conflict: %d %s", code, body)
	}

	finished := h.wait(t, j.ID)
	if finished.State != "CANCELED" {
		t.Fatalf("job state=%s stop_reason=%s", finished.State, finished.StopReason)
	}
	var count int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM control_operations WHERE principal_id='owner' AND operation_id='cancel-op-1'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("control operation rows=%d", count)
	}
}

func TestAgentControlRejectsStaleGenerationBeforeMutation(t *testing.T) {
	h := newJobHarness(t, true)
	spec := h.spec("single")
	spec.Input.Text = "slow-job"

	j, err := h.s.SubmitJob(h.ctx, "control-stale-generation", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	session := waitControlSession(t, h, j.ID)

	req := SessionControlRequest{
		OperationID:        "stale-op-1",
		ExpectedAttemptID:  session.AttemptID,
		ExpectedGeneration: session.Generation + 1,
		Reason:             "stale client",
	}
	raw, _ := json.Marshal(req)
	path := "/v1/jobs/" + j.ID + "/sessions/" + session.SessionID + "/cancel"
	code, body := h.request(t, http.MethodPost, path, "", raw)
	if code != http.StatusConflict || !jsonBodyHasErrorCode(body, "ATTEMPT_FENCED") {
		t.Fatalf("stale generation: %d %s", code, body)
	}

	var count int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM control_operations WHERE operation_id='stale-op-1'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fenced operation persisted count=%d", count)
	}

	if _, err = h.s.CancelJob(h.ctx, j.ID, CancelJobRequest{ControlID: "cleanup-stale-job", Reason: "test cleanup"}); err != nil {
		t.Fatal(err)
	}
	if finished := h.wait(t, j.ID); finished.State != "CANCELED" {
		t.Fatalf("cleanup state=%s", finished.State)
	}
}

func TestAgentControlUnsupportedInteractiveActionsFailClosed(t *testing.T) {
	h := newJobHarness(t, true, func(c *config.Server) {
		c.Users[0].Scopes = append(c.Users[0].Scopes, "jobs:control")
	})
	spec := h.spec("single")
	spec.Input.Text = "slow-job"

	j, err := h.s.SubmitJob(h.ctx, "control-unsupported-input", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	session := waitControlSession(t, h, j.ID)
	req := SessionControlRequest{
		OperationID:        "input-op-1",
		ExpectedAttemptID:  session.AttemptID,
		ExpectedGeneration: session.Generation,
		Mode:               "queue_next",
		Content:            "continue",
	}
	raw, _ := json.Marshal(req)

	for _, suffix := range []string{"inputs", "interrupt"} {
		code, body := h.request(t, http.MethodPost,
			"/v1/jobs/"+j.ID+"/sessions/"+session.SessionID+"/"+suffix, "", raw)
		if code != http.StatusUnprocessableEntity || !jsonBodyHasErrorCode(body, "CAPABILITY_UNSUPPORTED") {
			t.Fatalf("%s: %d %s", suffix, code, body)
		}
	}

	if _, err = h.s.CancelJob(h.ctx, j.ID, CancelJobRequest{ControlID: "cleanup-unsupported-job", Reason: "test cleanup"}); err != nil {
		t.Fatal(err)
	}
	if finished := h.wait(t, j.ID); finished.State != "CANCELED" {
		t.Fatalf("cleanup state=%s", finished.State)
	}
}

func jsonBodyHasErrorCode(body []byte, want string) bool {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return payload.Error.Code == want
}
