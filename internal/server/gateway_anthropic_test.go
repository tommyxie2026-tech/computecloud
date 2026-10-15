package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
)

func TestAnthropicGatewayAdmissionRejectsUnsupportedOrUnboundedRequests(t *testing.T) {
	valid := `{"model":"claude-safe","max_tokens":256,"messages":[{"role":"user","content":"hello"}],"stream":false}`
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"unknown field", `{"model":"claude-safe","max_tokens":256,"messages":[{"role":"user","content":"hello"}],"tenant":"other"}`, "UNSUPPORTED_ANTHROPIC_FIELD"},
		{"wrong model", `{"model":"claude-other","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}`, "MODEL_NOT_ALLOWED"},
		{"missing max tokens", `{"model":"claude-safe","messages":[{"role":"user","content":"hello"}]}`, "MAX_TOKENS_REQUIRED"},
		{"invalid max tokens", `{"model":"claude-safe","max_tokens":0,"messages":[{"role":"user","content":"hello"}]}`, "INVALID_MAX_TOKENS"},
		{"bad stream flag", `{"model":"claude-safe","max_tokens":256,"messages":[{"role":"user","content":"hello"}],"stream":"yes"}`, "INVALID_STREAM_FLAG"},
		{"invalid role", `{"model":"claude-safe","max_tokens":256,"messages":[{"role":"system","content":"hello"}]}`, "INVALID_MESSAGE_ROLE"},
		{"missing messages", `{"model":"claude-safe","max_tokens":256,"messages":[]}`, "MESSAGES_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decodeAnthropicRequest([]byte(tc.body), 1<<20, "claude-safe")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
		})
	}
	if _, _, err := decodeAnthropicRequest([]byte(valid), 8, "claude-safe"); err == nil || err.Error() != "ANTHROPIC_REQUEST_TOO_LARGE" {
		t.Fatalf("oversized request error=%v", err)
	}
}

func TestAnthropicGatewayAdmissionClampsAndPreservesAllowedFields(t *testing.T) {
	body := `{"model":"claude-safe","max_tokens":4096,"messages":[{"role":"user","content":"hello"}],"stream":true,"metadata":{"user_id":"attempt-scoped"},"thinking":{"type":"enabled","budget_tokens":512}}`
	request, fields, err := decodeAnthropicRequest([]byte(body), 1<<20, "claude-safe")
	if err != nil {
		t.Fatal(err)
	}
	if request.Model != "claude-safe" || !request.Stream || request.MaxTokens != 4096 {
		t.Fatalf("decoded request: %+v", request)
	}
	if !strings.Contains(string(fields["thinking"]), "budget_tokens") {
		t.Fatalf("approved field lost: %s", fields["thinking"])
	}
}

func TestAnthropicGatewayUsageIncludesPromptCacheTokens(t *testing.T) {
	out := gatewayOutcome{}
	out.readAnthropicUsage([]byte(`{"usage":{"input_tokens":10,"cache_creation_input_tokens":3,"cache_read_input_tokens":5,"output_tokens":7}}`))
	if !out.known || out.input == nil || *out.input != 18 || out.output == nil || *out.output != 7 {
		t.Fatalf("Anthropic usage not conservatively settled: %+v", out)
	}
	unknown := gatewayOutcome{}
	unknown.readAnthropicUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":-1}}`))
	if unknown.known {
		t.Fatalf("invalid usage treated as complete: %+v", unknown)
	}
}

func anthropicGatewayFixture(t *testing.T, upstreamHandler http.HandlerFunc) (*jobHarness, *httptest.Server, string) {
	t.Helper()
	key := "anthropic-upstream-key-fixture-only"
	keyFile := testutil.Token(t, key)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		upstreamHandler(w, r)
	}))
	t.Cleanup(upstream.Close)
	h := newJobHarness(t, false, func(c *config.Server) {
		c.PublicExecutionMode = "isolated_only"
		c.Jobs.Templates = append(c.Jobs.Templates, config.JobTemplate{RuntimeProfile: "claude_http", PolicyRef: "review", AcceptanceProfile: "check", Digest: config.TemplateDigest("2.1.292", config.Policy{CodexSandbox: "read-only", ClaudePermissionMode: "dontAsk", ClaudeAllowedTools: []string{"Read"}}, [][]string{{"test", "-f", "README.md"}})})
		c.ModelGateway = config.ModelGateway{
			Enabled: true, PublicBaseURL: "https://gateway.example.invalid/v1", MaxRequestBytes: 4096,
			MaxInflight: 4, ConnectTimeoutSeconds: 2, HeaderTimeoutSeconds: 2,
			StreamIdleTimeoutSeconds: 5, RequestTimeoutSeconds: 10,
			Routes: map[string]config.ModelRoute{"anthropic": {
				Backend: "anthropic_messages", BaseURL: upstream.URL + "/v1", APIKeyFile: keyFile,
				APIKeyRef: "test-provider-key", ProtocolVersion: "2023-06-01", AllowedModels: []string{"claude-safe"},
				MaxInflight: 2, MaxAttemptTokens: 20000, MaxAttemptCostMicros: 500000,
				InputCostPerMillionTokensMicros: 3000000, OutputCostPerMillionTokensMicros: 15000000,
			}},
			CredentialRoutes: map[string]string{"account": "anthropic"},
		}
	})
	g := h.s.modelHandler.(*modelGateway)
	g.anthropicClient.Transport.(*http.Transport).TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	spec := h.spec("single")
	spec.Execution.Engine = "claude"
	spec.Execution.RuntimeProfile = "claude_http"
	spec.Execution.Model = "claude-safe"
	spec.Execution.Environment = "container"
	j, err := h.s.SubmitJob(h.ctx, "anthropic-gateway-fixture", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	var template config.JobTemplate
	for _, candidate := range h.s.cfg.Jobs.Templates {
		if candidate.RuntimeProfile == "claude_http" && candidate.PolicyRef == "review" && candidate.AcceptanceProfile == "check" {
			template = candidate
			break
		}
	}
	if template.Digest == "" {
		t.Fatal("claude_http fixture template missing")
	}
	peer := &session{hello: &pb.WorkerHello{
		WorkerId: "w1", Epoch: "anthropic-fixture", Slots: 1,
		Runtimes: []*pb.Runtime{{Profile: template.RuntimeProfile, Version: "2.1.292", Models: []string{"claude-safe"}, Credentials: []string{"account"}, Repositories: []string{"repo"}, Policies: []string{"review"}, Verifiers: []string{"check"}, Capabilities: []string{"event_stream", "cancel", "job_io_v1", "gateway_anthropic_messages_v1", "environment:container"}, TemplateDigests: map[string]string{template.Key(): template.Digest}}},
	}, identity: h.s.cfg.Workers[0]}
	if _, err = h.s.db.SQL.Exec("INSERT INTO workers VALUES(?,?,?,?)", "w1", "anthropic-fixture", encode(peer.hello), store.Now()); err != nil {
		t.Fatal(err)
	}
	var task string
	if err = h.s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if err = h.s.assign(h.ctx, task, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = h.s.db.SQL.QueryRow("SELECT body FROM commands WHERE task=? AND kind='start'", task).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	command := new(pb.Command)
	if err = decode(raw, command); err != nil {
		t.Fatal(err)
	}
	return h, upstream, modelAttemptToken(command.Assignment)
}

func TestAnthropicGatewayMessagesRewritesCredentialAndSettlesUsage(t *testing.T) {
	var calls atomic.Int32
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.URL.RawQuery != "" {
			t.Errorf("upstream request path/method/query = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "anthropic-upstream-key-fixture-only-012345678901234567890123456789" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("anthropic-beta") != "claude-code-20250219" {
			t.Errorf("upstream credential/protocol headers incorrect")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var sent map[string]json.RawMessage
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Error(err)
		}
		var maxTokens int64
		if err := json.Unmarshal(sent["max_tokens"], &maxTokens); err != nil || maxTokens < 1 || maxTokens >= 19999 {
			t.Errorf("Attempt token budget was not clamped: %s", body)
		}
		jsonResponse(w, http.StatusOK, map[string]any{
			"type": "message", "id": "msg_fixture", "model": "claude-safe", "content": []any{},
			"usage": map[string]int{"input_tokens": 10, "cache_creation_input_tokens": 3, "cache_read_input_tokens": 5, "output_tokens": 7},
		})
	})
	request := func(path, body string, add func(http.Header)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
		if add != nil {
			add(req.Header)
		}
		response := httptest.NewRecorder()
		h.s.modelHandler.ServeHTTP(response, req)
		return response
	}
	response := request("/v1/messages?beta=true", `{"model":"claude-safe","max_tokens":19999,"messages":[{"role":"user","content":"hello"}]}`, func(h http.Header) { h.Set("Anthropic-Beta", "claude-code-20250219") })
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"type":"message"`) {
		t.Fatalf("Messages proxy: %d %s", response.Code, response.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d want=1", calls.Load())
	}
	var input, output int64
	var complete int
	if err := h.s.db.SQL.QueryRow("SELECT input_tokens,output_tokens,usage_complete FROM gateway_requests WHERE attempt_id IS NOT NULL ORDER BY started DESC LIMIT 1").Scan(&input, &output, &complete); err != nil {
		t.Fatal(err)
	}
	if input != 18 || output != 7 || complete != 1 {
		t.Fatalf("stored usage input=%d output=%d complete=%d", input, output, complete)
	}
}

func TestAnthropicBetaHeaderIsExplicitlyAllowlisted(t *testing.T) {
	if !validAnthropicBetas("claude-code-20250219, context-management-2025-06-27") {
		t.Fatal("known CLI beta features rejected")
	}
	if !validAnthropicBetas("claude-code-20250219, mid-conversation-system-2026-04-07") {
		t.Fatal("Claude CLI mid-conversation system beta rejected")
	}
	for _, header := range []string{"unknown-feature", "claude-code-20250219,unknown-feature", "claude-code-20250219,claude-code-20250219", ","} {
		if validAnthropicBetas(header) {
			t.Errorf("accepted beta header %q", header)
		}
	}
}

func TestAnthropicGatewayRejectsUnsafeHeadersAndQueryBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		jsonResponse(w, http.StatusOK, map[string]any{"type": "message", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	})
	for _, tc := range []struct {
		name string
		path string
		add  func(http.Header)
	}{
		{"duplicate authorization", "/v1/messages", func(h http.Header) { h.Add("Authorization", "Bearer duplicate") }},
		{"client provider key", "/v1/messages", func(h http.Header) { h.Set("X-API-Key", "provider-secret-must-not-be-used") }},
		{"unsupported beta header", "/v1/messages", func(h http.Header) { h.Set("Anthropic-Beta", "unreviewed-feature") }},
		{"duplicate version", "/v1/messages", func(h http.Header) { h.Add("Anthropic-Version", "2023-06-01") }},
		{"arbitrary query", "/v1/messages?beta=true&tenant=x", nil},
		{"duplicate beta query", "/v1/messages?beta=true&beta=true", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"model":"claude-safe","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Anthropic-Version", "2023-06-01")
			req.Header.Set("Content-Type", "application/json")
			if tc.add != nil {
				tc.add(req.Header)
			}
			response := httptest.NewRecorder()
			h.s.modelHandler.ServeHTTP(response, req)
			if response.Code < 400 {
				t.Fatalf("unsafe request status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected requests reached upstream %d times", calls.Load())
	}
}

func TestAnthropicRouteRejectsOpenAIEndpoints(t *testing.T) {
	var calls atomic.Int32
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		jsonResponse(w, http.StatusOK, map[string]any{"type": "message", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-safe","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.s.modelHandler.ServeHTTP(response, req)
	if response.Code < 400 || calls.Load() != 0 {
		t.Fatalf("Anthropic route accepted a Responses API request: status=%d calls=%d", response.Code, calls.Load())
	}
}

func TestAnthropicGatewayUnknownUsageFailsClosed(t *testing.T) {
	var calls atomic.Int32
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		jsonResponse(w, http.StatusOK, map[string]any{"type": "message", "usage": map[string]int{"input_tokens": 5}})
	})
	body := `{"model":"claude-safe","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		h.s.modelHandler.ServeHTTP(response, req)
		want := http.StatusBadGateway
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("unknown usage request %d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unknown usage should block next dispatch; upstream calls=%d", calls.Load())
	}
}

func TestAnthropicGatewayRedirectIsNotFollowed(t *testing.T) {
	var redirected atomic.Int32
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/capture" {
			redirected.Add(1)
			return
		}
		w.Header().Set("Location", "https://gateway.example.invalid/capture")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-safe","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.s.modelHandler.ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || redirected.Load() != 0 || strings.Contains(response.Body.String(), "gateway.example.invalid") {
		t.Fatalf("redirect was followed or leaked: status=%d redirected=%d body=%s", response.Code, redirected.Load(), response.Body.String())
	}
}

func TestAnthropicGatewayRedactsUpstreamErrorsAndNeverPersistsPromptOrAttemptToken(t *testing.T) {
	providerKey := "anthropic-upstream-key-fixture-only-012345678901234567890123456789"
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"provider failure `+providerKey+` prompt-canary"}`)
	})
	prompt := "request prompt-canary"
	body := `{"model":"claude-safe","max_tokens":100,"messages":[{"role":"user","content":"` + prompt + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.s.modelHandler.ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), providerKey) || strings.Contains(response.Body.String(), prompt) || strings.Contains(response.Body.String(), token) {
		t.Fatalf("upstream failure leaked sensitive data: status=%d body=%s", response.Code, response.Body.String())
	}
	var stored []byte
	if err := h.s.db.SQL.QueryRow(`SELECT CAST(model AS BLOB)||CAST(endpoint AS BLOB)||CAST(state AS BLOB)||CAST(error_code AS BLOB) FROM gateway_requests WHERE attempt_id IS NOT NULL ORDER BY started DESC LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{providerKey, prompt, token} {
		if bytes.Contains(stored, []byte(secret)) {
			t.Fatalf("Gateway evidence persisted secret %q", secret)
		}
	}
}

func TestAnthropicGatewayBodyLimitAndModelMismatchNeverReachUpstream(t *testing.T) {
	var calls atomic.Int32
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		jsonResponse(w, http.StatusOK, map[string]any{"type": "message", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	})
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"model":"not-allowed","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`, http.StatusBadRequest},
		{`{"model":"claude-safe","max_tokens":100,"messages":[{"role":"user","content":"` + strings.Repeat("x", 5000) + `"}]}`, http.StatusRequestEntityTooLarge},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		h.s.modelHandler.ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached upstream %d times", calls.Load())
	}
}

func TestAnthropicSSEUsageAndFinalEventAreBounded(t *testing.T) {
	var out gatewayOutcome
	observer := &anthropicSSEUsage{out: &out}
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":2,\"cache_read_input_tokens\":3,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for i := 0; i < len(stream); i += 7 {
		end := i + 7
		if end > len(stream) {
			end = len(stream)
		}
		if err := observer.feed([]byte(stream[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	if err := observer.finish(); err != nil {
		t.Fatal(err)
	}
	if out.state != "COMPLETE" || !out.known || out.input == nil || *out.input != 15 || out.output == nil || *out.output != 7 {
		t.Fatalf("settled stream usage: %+v", out)
	}

	bad := &anthropicSSEUsage{out: &gatewayOutcome{}}
	if err := bad.feed([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")); err == nil {
		t.Fatal("invalid message_start accepted")
	}
	oversized := &anthropicSSEUsage{out: &gatewayOutcome{}}
	if err := oversized.feed(append([]byte("data: "), bytes.Repeat([]byte("x"), anthropicSSEEventLimit)...)); err == nil {
		t.Fatal("oversized SSE event accepted")
	}
}

func TestAnthropicGatewayStreamSettlesOnlyAfterMessageStop(t *testing.T) {
	var calls atomic.Int32
	h, _, token := anthropicGatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		requestBody, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"usage\":{\"input_tokens\":8,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
		if bytes.Contains(requestBody, []byte("partial")) {
			return
		}
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	request := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		h.s.modelHandler.ServeHTTP(response, req)
		return response
	}
	response := request("/v1/messages", `{"model":"claude-safe","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: message_stop") {
		t.Fatalf("stream response status=%d body=%s", response.Code, response.Body.String())
	}
	var input, output int64
	var complete int
	if err := h.s.db.SQL.QueryRow("SELECT input_tokens,output_tokens,usage_complete FROM gateway_requests WHERE attempt_id IS NOT NULL ORDER BY started DESC LIMIT 1").Scan(&input, &output, &complete); err != nil {
		t.Fatal(err)
	}
	if input != 8 || output != 5 || complete != 1 {
		t.Fatalf("stream usage input=%d output=%d complete=%d", input, output, complete)
	}
	partial := request("/v1/messages?beta=true", `{"model":"claude-safe","max_tokens":100,"messages":[{"role":"user","content":"partial"}],"stream":true}`)
	if partial.Code != http.StatusOK {
		t.Fatalf("partial stream status=%d", partial.Code)
	}
	var unknown int
	if err := h.s.db.SQL.QueryRow("SELECT count(*) FROM gateway_requests WHERE attempt_id IS NOT NULL AND usage_complete=0").Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 1 || calls.Load() != 2 {
		t.Fatalf("partial stream settlement unknown=%d upstream_calls=%d", unknown, calls.Load())
	}
}
