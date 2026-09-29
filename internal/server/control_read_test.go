package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

func TestAgentControlReadBootstrapSessionsAndReplay(t *testing.T) {
	h := newJobHarness(t, true)
	spec := h.spec("single")
	j, err := h.s.SubmitJob(h.ctx, "control-read", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	finished := h.wait(t, j.ID)
	if finished.State != "SUCCEEDED" {
		t.Fatalf("job state=%s", finished.State)
	}

	code, body := h.request(t, "GET", "/v1/control/bootstrap", "", nil)
	if code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", code, body)
	}
	var bootstrap ControlBootstrap
	if err = json.Unmarshal(body, &bootstrap); err != nil {
		t.Fatal(err)
	}
	if bootstrap.ProtocolMin != control.ProtocolV1Alpha1 || bootstrap.ProtocolMax != control.ProtocolV1Alpha1 || bootstrap.ServerEpoch == "" || !bootstrap.ReadOnly {
		t.Fatalf("invalid read-only bootstrap: %+v", bootstrap)
	}
	if len(bootstrap.Runtimes) == 0 {
		t.Fatal("runtime matrix empty")
	}

	code, body = h.request(t, "GET", "/v1/jobs/"+j.ID+"/sessions", "", nil)
	if code != http.StatusOK {
		t.Fatalf("sessions: %d %s", code, body)
	}
	var sessions struct {
		Sessions []control.AgentSession `json:"sessions"`
	}
	if err = json.Unmarshal(body, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Sessions) != 1 {
		t.Fatalf("sessions=%d body=%s", len(sessions.Sessions), body)
	}
	session := sessions.Sessions[0]
	if session.SessionID != session.AttemptID || session.Generation < 1 || session.Runtime != "codex_exec" || session.State != control.SessionCompleted {
		t.Fatalf("invalid session projection: %+v", session)
	}
	if session.RuntimeSessionRef == "" {
		t.Fatal("native runtime session was not projected")
	}
	if !containsControlCapability(session.Capabilities, control.CapabilityStreamOutput) ||
		!containsControlCapability(session.Capabilities, control.CapabilityStructuredOutput) {
		t.Fatalf("read capabilities missing: %v", session.Capabilities)
	}
	code, body = h.request(t, "GET", "/v1/jobs/"+j.ID+"/sessions/"+session.SessionID, "", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(session.AttemptID)) {
		t.Fatalf("single session: %d %s", code, body)
	}

	events, err := h.s.JobEvents(h.ctx, j.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeSeq int64
	for _, event := range events.Events {
		if event.Type == "control.runtime_event" {
			runtimeSeq = event.Seq
			break
		}
	}
	if runtimeSeq == 0 {
		t.Fatalf("runtime events were not mirrored to job replay: %+v", events.Events)
	}

	req, _ := http.NewRequestWithContext(h.ctx, "GET", h.url+"/v1/jobs/"+j.ID+"/events/stream?after_seq=0", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	res, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("SSE status=%d content-type=%q body=%s", res.StatusCode, res.Header.Get("Content-Type"), b)
	}
	scanner := bufio.NewScanner(res.Body)
	var ids []int64
	foundRuntime := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			id, parseErr := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			ids = append(ids, id)
		}
		if line == "event: control.runtime_event" {
			foundRuntime = true
		}
	}
	res.Body.Close()
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 || !foundRuntime {
		t.Fatalf("SSE did not replay expected events ids=%v runtime=%v", ids, foundRuntime)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			t.Fatalf("SSE gap %d -> %d", ids[i-1], ids[i])
		}
	}

	// Last-Event-ID must resume strictly after the supplied durable cursor.
	resumeFrom := ids[len(ids)/2]
	req, _ = http.NewRequestWithContext(h.ctx, "GET", h.url+"/v1/jobs/"+j.ID+"/events/stream", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(resumeFrom, 10))
	res, err = h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner = bufio.NewScanner(res.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "id: ") {
			id, _ := strconv.ParseInt(strings.TrimPrefix(scanner.Text(), "id: "), 10, 64)
			if id <= resumeFrom {
				t.Fatalf("replayed stale SSE event %d <= %d", id, resumeFrom)
			}
		}
	}
}

func TestAgentControlReadSessionStateMapping(t *testing.T) {
	cases := map[string]control.SessionState{
		"STARTING": control.SessionStarting,
		"RUNNING": control.SessionRunning,
		"VERIFYING": control.SessionRunning,
		"CANCELING": control.SessionInterrupting,
		"RECONCILING": control.SessionUnverifiable,
		"SUCCEEDED": control.SessionCompleted,
		"FAILED": control.SessionFailed,
		"CANCELED": control.SessionCanceled,
		"QUEUED": control.SessionCreated,
	}
	for input, expected := range cases {
		if got := controlSessionState(input); got != expected {
			t.Fatalf("%s -> %s want %s", input, got, expected)
		}
	}
}

func containsControlCapability(values []control.Capability, value control.Capability) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}


func TestAgentControlBootstrapWriteScope(t *testing.T) {
	h := newJobHarness(t, false, func(cfg *config.Server) {
		cfg.Users[0].Scopes = append(cfg.Users[0].Scopes, "jobs:control")
	})
	bootstrap, err := h.s.ControlBootstrap(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.ReadOnly {
		t.Fatalf("jobs:control principal projected read-only: %+v", bootstrap)
	}
}
