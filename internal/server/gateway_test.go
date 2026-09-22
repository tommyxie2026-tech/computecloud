package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
)

func TestGatewayForwardingUsageFailuresAndBinding(t *testing.T) {
	var calls atomic.Int32
	streamStarted := make(chan struct{}, 2)
	streamEnded := make(chan struct{}, 2)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer upstream-012345678901234567890123456789" {
			t.Error("upstream credential not replaced")
		}
		if r.Header.Get("X-Job-ID") != "" {
			t.Error("untrusted attribution forwarded")
		}
		b, _ := io.ReadAll(r.Body)
		var input map[string]json.RawMessage
		if e := json.Unmarshal(b, &input); e != nil {
			t.Error(e)
		}
		if r.URL.Path == "/v1/responses/compact" {
			if _, ok := input["store"]; ok {
				t.Error("store added to compact")
			}
			jsonResponse(w, 200, map[string]any{"output": []any{map[string]string{"type": "compaction", "encrypted_content": "opaque-token"}}, "usage": map[string]int{"input_tokens": 4, "output_tokens": 2}})
			return
		}
		if string(input["store"]) != "false" {
			t.Error("store must be false")
		}
		switch string(input["input"]) {
		case `"wait"`:
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(": pending\n\n"))
			w.(http.Flusher).Flush()
			streamStarted <- struct{}{}
			<-r.Context().Done()
			streamEnded <- struct{}{}
		case `"rate"`:
			w.Header().Set("Retry-After", "7")
			jsonResponse(w, 429, map[string]any{"error": map[string]string{"message": "fixture rate limit"}})
		case `"stream"`, `"half"`:
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"custom_tool_call\",\"call_id\":\"call-1\",\"input\":\"opaque\"}}\n\n"))
			w.(http.Flusher).Flush()
			if string(input["input"]) == `"stream"` {
				w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":5}}}\n\n"))
			}
		case `"huge"`:
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: " + strings.Repeat("x", (4<<20)+10) + "\n\n"))
		default:
			jsonResponse(w, 200, map[string]any{"id": "fixture-response", "input": input["input"], "tools": input["tools"], "usage": map[string]int{"input_tokens": 7, "output_tokens": 3}})
		}
	}))
	defer upstream.Close()
	user, model, wt := testutil.Token(t, "job"), testutil.Token(t, "model"), testutil.Token(t, "worker")
	wc := config.Worker{Runtimes: map[string]config.Runtime{"codex_exec": {Version: "fixture-1"}}, Policies: map[string]config.Policy{"review": {CodexSandbox: "read-only"}}, Verifiers: map[string][][]string{"check": {}}}
	cfg := config.Server{DataDir: t.TempDir(), TLS: config.TLS{InsecureLoopback: true}, LeaseSeconds: 6, TickMS: 20, MaxArtifactBytes: 32 << 20, MaxProjectTasks: 4, Credentials: map[string]int{"account": 2}, Users: []config.Identity{{TokenFile: user, Owner: "owner", Projects: []string{"project"}, Credentials: []string{"account"}, Scopes: []string{"jobs:submit", "jobs:read", "jobs:cancel"}}, {TokenFile: model, Owner: "owner", Projects: []string{"project"}, Scopes: []string{"models:invoke"}, ModelProject: "project", ModelRoute: "route"}}, Workers: []config.Identity{{TokenFile: wt, WorkerID: "w", Projects: []string{"project"}, Credentials: []string{"account"}}}, Jobs: config.Jobs{Enabled: true, Templates: config.Templates(wc)}, ModelGateway: config.ModelGateway{Enabled: true, PublicBaseURL: "http://127.0.0.1:7444/v1", MaxInflight: 2, Routes: map[string]config.ModelRoute{"route": {BaseURL: upstream.URL + "/v1", APIKeyFile: testutil.Token(t, "upstream"), AllowedModels: []string{"model-c"}, MaxInflight: 2}}, CredentialRoutes: map[string]string{"account": "route"}}}
	s, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	g := s.modelHandler.(*modelGateway)
	g.client.Transport.(*http.Transport).TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	hs := httptest.NewServer(s.HTTPHandler())
	defer hs.Close()
	tok, _ := config.Token(model)
	req := func(path, body string) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Job-ID", "forged")
		res, e := hs.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, b
	}
	code, b := req("/v1/responses", `{"model":"model-c","input":[{"type":"function_call_output","call_id":"call-1","output":"unchanged"}],"tools":[{"type":"custom","name":"patch","format":{"type":"text"}}]}`)
	if code != 200 || !bytes.Contains(b, []byte("function_call_output")) || !bytes.Contains(b, []byte("call-1")) || !bytes.Contains(b, []byte("custom")) {
		t.Fatalf("tool fidelity: %d %s", code, b)
	}
	code, b = req("/v1/responses/compact", `{"model":"model-c","input":[]}`)
	if code != 200 || !bytes.Contains(b, []byte("opaque-token")) {
		t.Fatalf("compaction: %d %s", code, b)
	}
	code, b = req("/v1/responses", `{"model":"model-c","input":"stream","stream":true}`)
	if code != 200 || !bytes.Contains(b, []byte("custom_tool_call")) || !bytes.Contains(b, []byte("response.completed")) {
		t.Fatalf("SSE: %d %s", code, b)
	}
	before := calls.Load()
	code, _ = req("/v1/responses", `{"model":"model-c","input":"rate"}`)
	if code != 429 || calls.Load() != before+1 {
		t.Fatal("upstream error retried or rewritten")
	}
	code, b = req("/v1/responses", `{"model":"model-c","input":"half","stream":true}`)
	if code != 200 || bytes.Contains(b, []byte("response.completed")) {
		t.Fatal("half stream became success")
	}
	req("/v1/responses", `{"model":"model-c","input":"huge","stream":true}`)
	var complete, unknown, unsettled int
	deadline := time.Now().Add(3 * time.Second)
	for {
		if e = s.db.SQL.QueryRow("SELECT count(*) FROM gateway_requests WHERE state='STARTED'").Scan(&unsettled); e != nil {
			t.Fatal(e)
		}
		if unsettled == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("usage did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.db.SQL.QueryRow("SELECT count(*) FROM gateway_requests WHERE usage_complete=1").Scan(&complete)
	s.db.SQL.QueryRow("SELECT count(*) FROM gateway_requests WHERE input_tokens IS NULL AND output_tokens IS NULL").Scan(&unknown)
	if complete != 3 || unknown != 3 {
		t.Fatalf("usage coverage complete=%d unknown=%d", complete, unknown)
	}
	before = calls.Load()
	for _, body := range []string{`{"model":"not-allowed"}`, `{"model":"model-c","store":true}`, `{"model":"model-c","previous_response_id":"other"}`, `{"model":"model-c","background":true}`, `{"model":"model-c","model":"model-c"}`} {
		code, _ = req("/v1/responses", body)
		if code < 400 {
			t.Fatal("invalid request accepted")
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid requests reached provider")
	}
	code, _ = req("/v1/jobs", `{}`)
	if code != 403 {
		t.Fatalf("model token accepted for jobs: %d", code)
	}
	// Bind one explicit Job Attempt. The token is absent from durable start commands.
	userToken, _ := config.Token(user)
	ctx, e := s.auth.Bearer(context.Background(), "Bearer "+userToken)
	if e != nil {
		t.Fatal(e)
	}
	h := &jobHarness{commit: strings.Repeat("a", 40)}
	spec := h.spec("single")
	j, e := s.SubmitJob(ctx, "bound", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	peer := &session{hello: &pb.WorkerHello{WorkerId: "w", Epoch: "epoch", Slots: 1, Runtimes: []*pb.Runtime{{Profile: "codex_exec", Models: []string{"model-c"}, Credentials: []string{"account"}, Repositories: []string{"repo"}, Policies: []string{"review"}, Verifiers: []string{"check"}, Capabilities: []string{"event_stream", "cancel", "job_io_v1", "gateway_inference_v1"}, TemplateDigests: map[string]string{cfg.Jobs.Templates[0].Key(): cfg.Jobs.Templates[0].Digest}}}}, identity: cfg.Workers[0]}
	if _, e = s.db.SQL.Exec("INSERT INTO workers VALUES(?,?,?,?)", "w", "epoch", encode(peer.hello), store.Now()); e != nil {
		t.Fatal(e)
	}
	var task string
	s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task)
	if e = s.assign(ctx, task, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e = s.db.SQL.QueryRow("SELECT body FROM commands WHERE task=? AND kind='start'", task).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	cmd := new(pb.Command)
	if e = decode(raw, cmd); e != nil {
		t.Fatal(e)
	}
	if cmd.Assignment.Gateway == nil || cmd.Assignment.Gateway.Token != "" {
		t.Fatal("model token persisted in command")
	}
	tok = modelAttemptToken(cmd.Assignment)
	code, _ = req("/v1/responses", `{"model":"model-c","input":"bound"}`)
	if code != 200 {
		t.Fatalf("valid attempt rejected: %d", code)
	}
	streamsDone := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req("/v1/responses", `{"model":"model-c","input":"wait","stream":true}`)
			streamsDone <- struct{}{}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-streamStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("stream did not start")
		}
	}
	before = calls.Load()
	code, _ = req("/v1/responses", `{"model":"model-c","input":"over capacity"}`)
	if code != 429 || calls.Load() != before {
		t.Fatal("concurrency limit bypassed")
	}
	if _, e = s.CancelJob(ctx, j.ID, CancelJobRequest{ControlID: "stop"}); e != nil {
		t.Fatal(e)
	}
	before = calls.Load()
	code, _ = req("/v1/responses", `{"model":"model-c","input":"after cancel"}`)
	if code != 403 || calls.Load() != before {
		t.Fatalf("revoked attempt reached provider: %d", code)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-streamEnded:
		case <-time.After(3 * time.Second):
			t.Fatal("canceled attempt retained upstream stream")
		}
		select {
		case <-streamsDone:
		case <-time.After(3 * time.Second):
			t.Fatal("downstream did not close")
		}
	}
	var jid, aid string
	if e = s.db.SQL.QueryRow("SELECT job_id,attempt_id FROM gateway_requests WHERE attempt_id IS NOT NULL").Scan(&jid, &aid); e != nil || jid != j.ID || aid != cmd.Assignment.AttemptId {
		t.Fatalf("attribution: %s %s %v", jid, aid, e)
	}
}

func TestGatewayWorkerInvocationAndSecretRedaction(t *testing.T) {
	var invoked atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		jsonResponse(w, 200, map[string]any{"usage": map[string]int{"input_tokens": 8, "output_tokens": 4}})
	}))
	defer upstream.Close()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := listener.Addr().String()
	listener.Close()
	h := newJobHarness(t, true, func(c *config.Server) {
		c.HTTP.Listen = addr
		c.ModelGateway = config.ModelGateway{Enabled: true, PublicBaseURL: "http://" + addr + "/v1", Routes: map[string]config.ModelRoute{"route": {BaseURL: upstream.URL + "/v1", APIKeyFile: testutil.Token(t, "upstream"), AllowedModels: []string{"model-c"}}}, CredentialRoutes: map[string]string{"account": "route"}}
	})
	g := h.s.modelHandler.(*modelGateway)
	g.client.Transport.(*http.Transport).TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	j, e := h.s.SubmitJob(h.ctx, "gateway-worker", job.JSON(h.spec("single")))
	if e != nil {
		t.Fatal(e)
	}
	got := h.wait(t, j.ID)
	if got.State != "SUCCEEDED" {
		res, _ := h.s.JobResult(h.ctx, j.ID)
		t.Fatalf("gateway job: %+v %s", got, res)
	}
	if invoked.Load() != 1 || got.Usage["coverage"] != "complete" || got.Usage["input_tokens"] != int64(8) {
		t.Fatalf("gateway usage: %v invocations=%d", got.Usage, invoked.Load())
	}
	var path string
	if e = h.s.db.SQL.QueryRow("SELECT a.path FROM artifacts a JOIN tasks t ON a.task=t.id WHERE t.job_id=?", j.ID).Scan(&path); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(h.s.cfg.DataDir, "artifacts", path))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("ccma_")) || !bytes.Contains(raw, []byte("[REDACTED]")) {
		t.Fatal("injected gateway token was not redacted")
	}
}
