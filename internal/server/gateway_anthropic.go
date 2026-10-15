package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type anthropicMessageRequest struct {
	Model     string
	MaxTokens int64
	Stream    bool
}

var anthropicMessageFields = []string{
	"model", "max_tokens", "messages", "system", "metadata", "stop_sequences", "stream",
	"temperature", "top_p", "top_k", "tools", "tool_choice", "thinking", "output_config",
	"context_management", "service_tier", "betas",
}

var anthropicBetaFeatures = []string{
	"claude-code-20250219", "interleaved-thinking-2025-05-14", "thinking-token-count-2026-05-13",
	"context-management-2025-06-27", "prompt-caching-scope-2026-01-05", "per-turn-control-2026-07-01",
	"mid-conversation-system-2026-04-07", "mid-conversation-tool-changes-2026-07-01",
	"effort-2025-11-24", "thinking-display-updates-2026-08-18",
}

func decodeAnthropicRequest(body []byte, maxBytes int, expectedModel string) (anthropicMessageRequest, map[string]json.RawMessage, error) {
	var request anthropicMessageRequest
	if len(body) == 0 || maxBytes < 1 || len(body) > maxBytes {
		return request, nil, fmt.Errorf("ANTHROPIC_REQUEST_TOO_LARGE")
	}
	if err := jsonutil.Validate(body); err != nil {
		return request, nil, fmt.Errorf("INVALID_ANTHROPIC_JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return request, nil, fmt.Errorf("ANTHROPIC_OBJECT_REQUIRED")
	}
	for field := range fields {
		if !config.Contains(anthropicMessageFields, field) {
			return request, nil, fmt.Errorf("UNSUPPORTED_ANTHROPIC_FIELD")
		}
	}
	if json.Unmarshal(fields["model"], &request.Model) != nil || request.Model == "" || request.Model != expectedModel {
		return request, nil, fmt.Errorf("MODEL_NOT_ALLOWED")
	}
	if len(fields["max_tokens"]) == 0 || json.Unmarshal(fields["max_tokens"], &request.MaxTokens) != nil {
		return request, nil, fmt.Errorf("MAX_TOKENS_REQUIRED")
	}
	if request.MaxTokens < 1 || request.MaxTokens > 1_000_000 {
		return request, nil, fmt.Errorf("INVALID_MAX_TOKENS")
	}
	if raw, ok := fields["stream"]; ok && json.Unmarshal(raw, &request.Stream) != nil {
		return request, nil, fmt.Errorf("INVALID_STREAM_FLAG")
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 {
		return request, nil, fmt.Errorf("MESSAGES_REQUIRED")
	}
	for _, message := range messages {
		var role string
		if json.Unmarshal(message["role"], &role) != nil || !config.Contains([]string{"user", "assistant"}, role) || len(message["content"]) == 0 {
			return request, nil, fmt.Errorf("INVALID_MESSAGE_ROLE")
		}
	}
	return request, fields, nil
}

func (g *modelGateway) serveAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "METHOD_NOT_ALLOWED")
		return
	}
	if r.URL.RawQuery != "" && r.URL.RawQuery != "beta=true" {
		writeAnthropicError(w, http.StatusNotFound, "invalid_request_error", "UNSUPPORTED_QUERY")
		return
	}
	if len(r.Header.Values("Authorization")) != 1 || len(r.Header.Values("X-API-Key")) != 0 {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "ATTEMPT_BEARER_REQUIRED")
		return
	}
	if len(r.Header.Values("Anthropic-Version")) != 1 || len(r.Header.Values("Anthropic-Beta")) > 1 || !validAnthropicBetas(r.Header.Get("Anthropic-Beta")) {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "UNSUPPORTED_ANTHROPIC_HEADERS")
		return
	}
	if !g.s.rateLimitAPIKeyLookup(w, r, r.Header.Get("Authorization")) {
		return
	}
	id, tokenHash, err := g.identity(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "INVALID_ATTEMPT_TOKEN")
		return
	}
	if tokenHash == "" || id.attempt == "" || g.s.cfg.PublicExecutionMode != "isolated_only" {
		writeAnthropicError(w, http.StatusForbidden, "permission_error", "ATTEMPT_TOKEN_REQUIRED")
		return
	}
	route := g.routes[id.route]
	if route == nil || route.config.Backend != "anthropic_messages" || r.Header.Get("Anthropic-Version") != route.config.ProtocolVersion {
		writeAnthropicError(w, http.StatusForbidden, "permission_error", "ANTHROPIC_ROUTE_UNAVAILABLE")
		return
	}
	if !g.s.rateLimitHTTP(w, r, r.Header.Get("Authorization")) {
		return
	}
	if id.model == "" || !config.Contains(route.config.AllowedModels, id.model) {
		writeAnthropicError(w, http.StatusForbidden, "permission_error", "MODEL_NOT_ALLOWED")
		return
	}
	if r.Header.Get("Content-Type") == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "JSON_REQUIRED")
		return
	}
	body, err := readJSONBody(w, r, g.s.cfg.ModelGateway.MaxRequestBytes)
	if err != nil {
		var tooLarge requestTooLargeError
		if errors.As(err, &tooLarge) {
			writeAnthropicError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "REQUEST_TOO_LARGE")
		} else {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "INVALID_REQUEST_BODY")
		}
		return
	}
	request, fields, err := decodeAnthropicRequest(body, int(g.s.cfg.ModelGateway.MaxRequestBytes), id.model)
	if err != nil || !config.Contains(route.config.AllowedModels, request.Model) {
		code := "INVALID_ANTHROPIC_REQUEST"
		if err != nil {
			code = err.Error()
		}
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", code)
		return
	}
	releaseAttempt, ok := g.acquireAttemptSlot(id.attempt)
	if !ok {
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "ATTEMPT_CONCURRENCY_LIMIT")
		return
	}
	defer releaseAttempt()
	identityKey := id.owner + "\x00" + id.project
	if slots := g.identitySlots[identityKey]; slots != nil {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "IDENTITY_CONCURRENCY_LIMIT")
			return
		}
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		gatewayBusy(w)
		return
	}
	select {
	case route.slots <- struct{}{}:
		defer func() { <-route.slots }()
	default:
		gatewayBusy(w)
		return
	}
	remaining, err := remainingAttemptBudget(r.Context(), g.s.db.SQL, id.attempt, route.config.MaxAttemptTokens, route.config.MaxAttemptCostMicros, route.config.InputCostPerMillionTokensMicros, route.config.OutputCostPerMillionTokensMicros)
	if err != nil {
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", cleanCode(err))
		return
	}
	body, err = applyAnthropicAttemptBudget(fields, body, remaining, route.config.InputCostPerMillionTokensMicros, route.config.OutputCostPerMillionTokensMicros)
	if err != nil {
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", cleanCode(err))
		return
	}
	deadline := time.Now().Add(time.Duration(g.s.cfg.ModelGateway.RequestTimeoutSeconds) * time.Second)
	if id.deadline > 0 && time.UnixMilli(id.deadline).Before(deadline) {
		deadline = time.UnixMilli(id.deadline)
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	rid := store.ID()
	var traceID string
	err = g.s.db.Tx(ctx, func(q store.Query) error {
		current, checkErr := g.attemptIdentity(ctx, q, tokenHash)
		if checkErr != nil {
			return checkErr
		}
		id = current
		traceID, checkErr = g.requestTrace(ctx, q, id, r, fields)
		if checkErr != nil {
			return checkErr
		}
		var jobID, attemptID, trace any
		jobID, attemptID = id.job, id.attempt
		if traceID != "" {
			trace = traceID
		}
		_, checkErr = q.ExecContext(ctx, "INSERT INTO gateway_requests(id,owner,project,job_id,attempt_id,route,model,endpoint,state,started,trace_id) VALUES(?,?,?,?,?,?,?,?,'STARTED',?,?)", rid, id.owner, id.project, jobID, attemptID, id.route, request.Model, "/v1/messages", store.Now(), trace)
		return checkErr
	})
	if err != nil {
		writeAnthropicError(w, http.StatusForbidden, "permission_error", "ATTEMPT_NOT_ACTIVE")
		return
	}
	w.Header().Set("X-Request-ID", rid)
	w.Header().Set("X-Computecloud-Trace-ID", traceID)
	w.Header().Set("Cache-Control", "no-store")
	outcome := gatewayOutcome{state: "UNKNOWN", code: "UPSTREAM_INTERRUPTED"}
	defer func() { g.record(rid, outcome) }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if _, checkErr := g.attemptIdentity(ctx, g.s.db.SQL, tokenHash); checkErr != nil {
					cancel()
					return
				}
			}
		}
	}()
	upstreamURL := strings.TrimRight(route.config.BaseURL, "/") + "/messages"
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		outcome.code = "UPSTREAM_REQUEST_INVALID"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", outcome.code)
		return
	}
	upstream.Header.Set("x-api-key", route.key)
	upstream.Header.Set("anthropic-version", route.config.ProtocolVersion)
	if beta := r.Header.Get("Anthropic-Beta"); beta != "" {
		upstream.Header.Set("anthropic-beta", beta)
	}
	upstream.Header.Set("Content-Type", "application/json")
	if request.Stream {
		upstream.Header.Set("Accept", "text/event-stream")
	} else {
		upstream.Header.Set("Accept", "application/json")
	}
	upstream.Header.Set("Accept-Encoding", "identity")
	res, err := g.anthropicClient.Do(upstream)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "UPSTREAM_UNAVAILABLE")
		return
	}
	defer res.Body.Close()
	if request.Stream {
		g.streamAnthropicMessages(w, r, res, &outcome)
		return
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		outcome.state = "FAILED"
		outcome.code = "UPSTREAM_REJECTED"
		statusCode := http.StatusBadGateway
		if res.StatusCode == http.StatusTooManyRequests {
			statusCode = http.StatusTooManyRequests
		}
		writeAnthropicError(w, statusCode, "api_error", outcome.code)
		return
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") || (res.Header.Get("Content-Encoding") != "" && res.Header.Get("Content-Encoding") != "identity") {
		outcome.code = "UPSTREAM_RESPONSE_INVALID"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", outcome.code)
		return
	}
	response, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil || len(response) > 16<<20 {
		outcome.code = "UPSTREAM_RESPONSE_LIMIT"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", outcome.code)
		return
	}
	var responseType struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(response, &responseType) != nil || responseType.Type != "message" {
		outcome.code = "UPSTREAM_RESPONSE_INVALID"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", outcome.code)
		return
	}
	outcome.readAnthropicUsage(response)
	if !outcome.known {
		outcome.code = "UPSTREAM_USAGE_UNKNOWN"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", outcome.code)
		return
	}
	outcome.state = "COMPLETE"
	outcome.code = ""
	if value := res.Header.Get("request-id"); value != "" && len(value) <= 128 {
		outcome.upstreamID = value
		w.Header().Set("X-Upstream-Request-ID", value)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err = w.Write(response); err != nil {
		outcome.state = "UNKNOWN"
		outcome.code = "DOWNSTREAM_DISCONNECTED"
	}
}

func validAnthropicBetas(header string) bool {
	if header == "" {
		return true
	}
	seen := map[string]bool{}
	for _, feature := range strings.Split(header, ",") {
		feature = strings.TrimSpace(feature)
		if feature == "" || seen[feature] || !config.Contains(anthropicBetaFeatures, feature) {
			return false
		}
		seen[feature] = true
	}
	return true
}

func writeAnthropicError(w http.ResponseWriter, statusCode int, kind, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": code},
	})
}
