package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConversationMessagesExecutesDurableWorkerJob(t *testing.T) {
	token := testutil.Token(t, "conversation-user")
	otherToken := testutil.Token(t, "other-conversation-user")
	configure := func(c *config.Server) {
		var template config.JobTemplate
		for _, candidate := range c.Jobs.Templates {
			if candidate.RuntimeProfile == "codex_exec" {
				template = candidate
				break
			}
		}
		if template.RuntimeProfile == "" {
			t.Fatal("codex template missing")
		}
		profile := config.ConversationProfile{ID: "safe", PublicModel: "agent-codex", ExecutionOwner: "owner", ProjectID: "project", Workspace: job.Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("0", 40)}, Execution: job.Execution{Engine: "codex", RuntimeProfile: template.RuntimeProfile, Model: "model-c", CredentialRef: "account", PolicyRef: template.PolicyRef, AcceptanceProfile: template.AcceptanceProfile}, Limits: job.Limits{TimeoutSeconds: 60, MaxAttemptsPerTask: 1}, MaxOutputTokens: 512, MaxOutputBytes: 8192, MaxInputBytes: 240000, MaxActive: 2}
		c.ConversationJobs = config.ConversationJobs{Enabled: true, LoopbackListen: "127.0.0.1:0", MaxRequestBytes: 256 << 10, Profiles: []config.ConversationProfile{profile}}
		c.Users = append(c.Users, config.Identity{TokenFile: token, Owner: "conv-user", Projects: []string{"project"}, ConversationProfile: "safe", Scopes: []string{"conversations:submit", "conversations:read", "conversations:cancel"}})
		c.Users = append(c.Users, config.Identity{TokenFile: otherToken, Owner: "other-conv-user", Projects: []string{"project"}, ConversationProfile: "safe", Scopes: []string{"conversations:submit", "conversations:read", "conversations:cancel"}})
	}
	h := newJobHarness(t, true, configure)
	// The fixture Worker repository commit is owned by the shared Job harness.
	h.s.cfg.ConversationJobs.Profiles[0].Workspace.BaseCommit = h.commit
	server := httptest.NewServer(h.s.conversationHTTP(true))
	defer server.Close()
	unauth, _ := http.NewRequest(http.MethodGet, server.URL+"/agent/v1/models", nil)
	unauth.Header.Set("Authorization", "Bearer invalid-token")
	unauthResp, e := server.Client().Do(unauth)
	if e != nil {
		t.Fatal(e)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid Key status=%d", unauthResp.StatusCode)
	}
	deniedBody := []byte(`{"model":"unconfigured-model","max_tokens":128,"messages":[{"role":"user","content":"should not run"}]}`)
	denied, _ := http.NewRequest(http.MethodPost, server.URL+"/agent/v1/messages", bytes.NewReader(deniedBody))
	denied.Header.Set("Content-Type", "application/json")
	denied.Header.Set("Authorization", "Bearer "+mustToken(t, token))
	denied.Header.Set("Idempotency-Key", "denied-profile-1")
	deniedResp, e := server.Client().Do(denied)
	if e != nil {
		t.Fatal(e)
	}
	deniedResp.Body.Close()
	if deniedResp.StatusCode != http.StatusForbidden {
		t.Fatalf("unconfigured profile status=%d", deniedResp.StatusCode)
	}
	for _, tc := range []struct {
		path string
		body string
	}{
		{"/agent/v1/messages", `{"model":"agent-codex","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`},
		{"/agent/v1/messages/count_tokens", `{}`},
		{"/agent/v1/responses/input_tokens", `{}`},
		{"/agent/v1/responses/compact", `{}`},
	} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+mustToken(t, token))
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("unsupported capability %s status=%d payload=%#v", tc.path, resp.StatusCode, payload)
		}
	}
	body := []byte(`{"model":"agent-codex","max_tokens":128,"messages":[{"role":"user","content":"What is my current model?"}]}`)
	call := func(key string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/agent/v1/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+mustToken(t, token))
		req.Header.Set("Idempotency-Key", key)
		res, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var data map[string]any
		if e = json.NewDecoder(res.Body).Decode(&data); e != nil {
			t.Fatal(e)
		}
		return res, data
	}
	started := time.Now()
	res, data := call("model-check-1")
	if res.StatusCode != 200 {
		t.Fatalf("status=%d response=%#v", res.StatusCode, data)
	}
	if data["model"] != "agent-codex" {
		t.Fatalf("model alias missing: %#v", data)
	}
	content := data["content"].([]any)[0].(map[string]any)
	if content["text"] != "single fixture completed" {
		t.Fatalf("Worker result not returned: %#v", data)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("fixture Job unexpectedly slow")
	}
	var originalID string
	if e = h.s.db.SQL.QueryRow("SELECT request_id FROM conversation_requests WHERE idem_key='model-check-1'").Scan(&originalID); e != nil {
		t.Fatal(e)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := server.URL + "/agent/v1/requests/" + originalID
		if method == http.MethodPost {
			path += "/cancel"
		}
		req, _ := http.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+mustToken(t, otherToken))
		foreign, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		foreign.Body.Close()
		if foreign.StatusCode != http.StatusNotFound {
			t.Fatalf("cross-identity %s status=%d", method, foreign.StatusCode)
		}
	}
	res2, data2 := call("model-check-1")
	if res2.StatusCode != 200 || data2["id"] != data["id"] {
		t.Fatalf("idempotency replay changed result: %d %#v", res2.StatusCode, data2)
	}
	body = []byte(`{"model":"agent-codex","max_tokens":128,"messages":[{"role":"user","content":"long-output"}]}`)
	longRes, longData := call("large-output-1")
	if longRes.StatusCode != 200 {
		t.Fatalf("large output status=%d %#v", longRes.StatusCode, longData)
	}
	largeText := longData["content"].([]any)[0].(map[string]any)["text"].(string)
	if len(largeText) != 6000 {
		t.Fatalf("complete Worker output lost: got %d bytes", len(largeText))
	}
	responsesBody := []byte(`{"model":"agent-codex","input":[{"role":"user","content":"Summarize the fixture result."}],"max_output_tokens":128,"stream":true}`)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/agent/v1/responses", bytes.NewReader(responsesBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mustToken(t, token))
	req.Header.Set("Idempotency-Key", "responses-check-1")
	stream, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Body.Close()
	streamBytes, e := io.ReadAll(stream.Body)
	if e != nil {
		t.Fatal(e)
	}
	if stream.StatusCode != 200 || !strings.Contains(string(streamBytes), "response.completed") || !strings.Contains(string(streamBytes), "single fixture completed") {
		t.Fatalf("Responses SSE failed: status=%d body=%s", stream.StatusCode, streamBytes)
	}
	body = []byte(`{"model":"agent-codex","max_tokens":128,"messages":[{"role":"user","content":"fail-job"}]}`)
	failed, failedData := call("failed-job-1")
	if failed.StatusCode != http.StatusBadRequest {
		t.Fatalf("failed Worker result status=%d %#v", failed.StatusCode, failedData)
	}
	body = []byte(`{"model":"agent-codex","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"long-job"}]}`)
	disconnectReq, _ := http.NewRequest(http.MethodPost, server.URL+"/agent/v1/messages", bytes.NewReader(body))
	disconnectReq.Header.Set("Content-Type", "application/json")
	disconnectReq.Header.Set("Authorization", "Bearer "+mustToken(t, token))
	disconnectReq.Header.Set("Idempotency-Key", "disconnect-continues-1")
	disconnected, e := server.Client().Do(disconnectReq)
	if e != nil {
		t.Fatal(e)
	}
	disconnected.Body.Close()
	disconnectedJob := disconnected.Header.Get("X-ComputeCloud-Job-ID")
	if disconnectedJob == "" {
		t.Fatal("missing durable Job id on SSE response")
	}
	deadline := time.Now().Add(10 * time.Second)
	var disconnectedState string
	for time.Now().Before(deadline) {
		if e = h.s.db.SQL.QueryRow("SELECT state FROM jobs WHERE id=?", disconnectedJob).Scan(&disconnectedState); e != nil {
			t.Fatal(e)
		}
		if terminal(disconnectedState) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if disconnectedState != "SUCCEEDED" {
		t.Fatalf("disconnect stopped durable Job: %s", disconnectedState)
	}
	body = []byte(`{"model":"agent-codex","max_tokens":128,"messages":[{"role":"user","content":"slow-job"}]}`)
	type callResult struct {
		status int
		data   map[string]any
	}
	finished := make(chan callResult, 1)
	go func() { r, d := call("slow-cancel-1"); finished <- callResult{r.StatusCode, d} }()
	var requestID, jobID string
	limit := time.Now().Add(8 * time.Second)
	for time.Now().Before(limit) {
		err := h.s.db.SQL.QueryRow("SELECT request_id,job_id FROM conversation_requests WHERE idem_key='slow-cancel-1'").Scan(&requestID, &jobID)
		if err == nil && jobID != "" {
			break
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if requestID == "" || jobID == "" {
		t.Fatal("slow request did not persist its Job mapping")
	}
	defer func() {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/agent/v1/requests/"+requestID+"/cancel", nil)
		req.Header.Set("Authorization", "Bearer "+mustToken(t, token))
		res, err := server.Client().Do(req)
		if err == nil {
			res.Body.Close()
		}
	}()
	observerCtx, e := h.s.auth.Bearer(context.Background(), "Bearer "+mustToken(t, token))
	if e != nil {
		t.Fatal(e)
	}
	slowRecord, slowProfile, e := h.s.loadConversation(observerCtx, requestID, "conversations:read")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.s.waitConversation(observerCtx, slowRecord, *slowProfile, 500*time.Millisecond); status.Code(e) != codes.DeadlineExceeded {
		t.Fatalf("observer deadline not honored: %v", e)
	}
	var beforeCancel string
	if e = h.s.db.SQL.QueryRow("SELECT state FROM jobs WHERE id=?", jobID).Scan(&beforeCancel); e != nil {
		t.Fatal(e)
	}
	if terminal(beforeCancel) {
		t.Fatalf("observer timeout changed durable Job state to %s", beforeCancel)
	}
	cancelReq, _ := http.NewRequest(http.MethodPost, server.URL+"/agent/v1/requests/"+requestID+"/cancel", nil)
	cancelReq.Header.Set("Authorization", "Bearer "+mustToken(t, token))
	cancelResp, e := server.Client().Do(cancelReq)
	if e != nil {
		t.Fatal(e)
	}
	cancelResp.Body.Close()
	if cancelResp.StatusCode != 202 {
		t.Fatalf("cancel status=%d", cancelResp.StatusCode)
	}
	limit = time.Now().Add(8 * time.Second)
	var state string
	for time.Now().Before(limit) {
		if e = h.s.db.SQL.QueryRow("SELECT state FROM jobs WHERE id=?", jobID).Scan(&state); e != nil {
			t.Fatal(e)
		}
		if terminal(state) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if state != "CANCELED" {
		t.Fatalf("cancelled Job did not reach terminal state: %s", state)
	}
	var unreleased int
	if e = h.s.db.SQL.QueryRow("SELECT count(*) FROM tasks t JOIN attempts a ON a.task=t.id WHERE t.job_id=? AND a.released=0", jobID).Scan(&unreleased); e != nil || unreleased != 0 {
		t.Fatalf("Worker cleanup not fenced: unreleased=%d err=%v", unreleased, e)
	}
	select {
	case result := <-finished:
		if result.status != http.StatusRequestTimeout {
			t.Fatalf("observer cancellation response=%d %#v", result.status, result.data)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("request observer did not finish after cancellation")
	}
	logConversationEvidence(t, h, []string{"model-check-1", "large-output-1", "responses-check-1", "failed-job-1", "disconnect-continues-1", "slow-cancel-1"})
}

func logConversationEvidence(t *testing.T, h *jobHarness, keys []string) {
	t.Helper()
	items := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		var rid, jid, state, jobState, result string
		if err := h.s.db.SQL.QueryRow("SELECT request_id,job_id,state FROM conversation_requests WHERE idem_key=?", key).Scan(&rid, &jid, &state); err != nil {
			t.Fatal(err)
		}
		if err := h.s.db.SQL.QueryRow("SELECT state FROM jobs WHERE id=?", jid).Scan(&jobState); err != nil {
			t.Fatal(err)
		}
		var tasks, events, cleanup int
		_ = h.s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=?", jid).Scan(&tasks)
		_ = h.s.db.SQL.QueryRow("SELECT count(*) FROM job_events WHERE job_id=?", jid).Scan(&events)
		_ = h.s.db.SQL.QueryRow("SELECT count(*) FROM tasks t JOIN attempts a ON a.task=t.id WHERE t.job_id=? AND a.released=0", jid).Scan(&cleanup)
		_ = h.s.db.SQL.QueryRow("SELECT result FROM tasks WHERE job_id=? ORDER BY created DESC LIMIT 1", jid).Scan(&result)
		workerRows, err := h.s.db.SQL.Query("SELECT DISTINCT a.worker FROM tasks t JOIN attempts a ON a.task=t.id WHERE t.job_id=? ORDER BY a.worker", jid)
		if err != nil {
			t.Fatal(err)
		}
		workers := []string{}
		for workerRows.Next() {
			var id string
			if err = workerRows.Scan(&id); err != nil {
				workerRows.Close()
				t.Fatal(err)
			}
			workers = append(workers, id)
		}
		if err = workerRows.Err(); err != nil {
			workerRows.Close()
			t.Fatal(err)
		}
		workerRows.Close()
		digest := sha256.Sum256([]byte(result))
		items = append(items, map[string]any{"request_id": rid, "job_id": jid, "request_state": state, "job_state": jobState, "task_count": tasks, "worker_ids": workers, "job_event_count": events, "unreleased_attempts": cleanup, "result_sha256": hex.EncodeToString(digest[:])})
	}
	b, _ := json.Marshal(items)
	t.Log("CI_EVIDENCE " + string(b))
}

func TestConversationHTTPSRequiresTLS13(t *testing.T) {
	s := &Server{cfg: config.Server{ConversationJobs: config.ConversationJobs{Enabled: true}}}
	h := s.conversationHTTP(false)
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		req := httptest.NewRequest(http.MethodGet, "https://server/agent/v1/models", nil)
		req.TLS = &tls.ConnectionState{Version: version}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if version == tls.VersionTLS12 && w.Code != http.StatusBadRequest {
			t.Fatalf("TLS 1.2 status=%d", w.Code)
		}
		if version == tls.VersionTLS13 && w.Code != http.StatusUnauthorized {
			t.Fatalf("TLS 1.3 should pass transport guard then require auth, got %d", w.Code)
		}
	}
}

func TestConversationOutputCapPreservesUTF8(t *testing.T) {
	got, truncated := limitConversationOutput("a界bc", 4)
	if !truncated || got != "a界" {
		t.Fatalf("unexpected UTF-8 truncation: %q %v", got, truncated)
	}
}

func mustToken(t *testing.T, path string) string {
	t.Helper()
	v, e := config.Token(path)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
