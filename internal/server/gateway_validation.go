package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validateModelBody(fields map[string]json.RawMessage, compact bool) error {
	allowed := []string{"model", "input", "instructions", "tools", "tool_choice", "parallel_tool_calls", "reasoning", "text", "include", "stream", "store", "background", "temperature", "top_p", "top_logprobs", "truncation", "max_output_tokens", "metadata", "service_tier", "safety_identifier", "prompt_cache_key", "prompt_cache_retention", "stream_options"}
	if compact {
		allowed = []string{"model", "input", "instructions"}
	}
	for key := range fields {
		if !config.Contains(allowed, key) {
			return fmt.Errorf("UNSUPPORTED_MODEL_FIELD")
		}
	}
	if raw, ok := fields["tools"]; ok {
		var tools []struct {
			Type string `json:"type"`
		}
		if e := json.Unmarshal(raw, &tools); e != nil {
			return fmt.Errorf("INVALID_TOOLS")
		}
		for _, tool := range tools {
			if tool.Type != "function" && tool.Type != "custom" {
				return fmt.Errorf("UNSUPPORTED_HOSTED_TOOL")
			}
		}
	}
	if raw, ok := fields["include"]; ok {
		var values []string
		if e := json.Unmarshal(raw, &values); e != nil {
			return fmt.Errorf("INVALID_INCLUDE")
		}
		for _, v := range values {
			if v != "reasoning.encrypted_content" && v != "message.output_text.logprobs" {
				return fmt.Errorf("UNSUPPORTED_INCLUDE")
			}
		}
	}
	if raw, ok := fields["tool_choice"]; ok {
		var simple string
		if json.Unmarshal(raw, &simple) == nil {
			if !config.Contains([]string{"auto", "none", "required"}, simple) {
				return fmt.Errorf("UNSUPPORTED_TOOL_CHOICE")
			}
		} else {
			var choice struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &choice) != nil || !config.Contains([]string{"function", "custom"}, choice.Type) {
				return fmt.Errorf("UNSUPPORTED_TOOL_CHOICE")
			}
		}
	}
	raw, ok := fields["input"]
	if !ok {
		return fmt.Errorf("INPUT_REQUIRED")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && string(raw) != "null" {
		return nil
	}
	var items []map[string]json.RawMessage
	if e := json.Unmarshal(raw, &items); e != nil || items == nil {
		return fmt.Errorf("INVALID_INPUT")
	}
	for _, item := range items {
		var typ string
		if raw, ok := item["type"]; ok {
			if json.Unmarshal(raw, &typ) != nil {
				return fmt.Errorf("INVALID_INPUT_TYPE")
			}
		}
		switch typ {
		case "", "message":
			var role string
			if json.Unmarshal(item["role"], &role) != nil || !config.Contains([]string{"user", "assistant", "system", "developer"}, role) {
				return fmt.Errorf("INVALID_MESSAGE_ROLE")
			}
			if e := textContent(item["content"]); e != nil {
				return e
			}
		case "function_call", "custom_tool_call", "reasoning", "compaction":
		case "function_call_output", "custom_tool_call_output":
			if e := textContent(item["output"]); e != nil {
				return e
			}
		default:
			return fmt.Errorf("UNSUPPORTED_INPUT_TYPE")
		}
	}
	return nil
}
func textContent(raw []byte) error {
	var text string
	if json.Unmarshal(raw, &text) == nil && string(raw) != "null" {
		return nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || parts == nil {
		return fmt.Errorf("INVALID_TEXT_CONTENT")
	}
	for _, p := range parts {
		var typ string
		if json.Unmarshal(p["type"], &typ) != nil || !config.Contains([]string{"input_text", "output_text", "refusal"}, typ) {
			return fmt.Errorf("UNSUPPORTED_MEDIA_CONTENT")
		}
	}
	return nil
}
func modelError(w http.ResponseWriter, e error) {
	code := 500
	typ := "server_error"
	switch status.Code(e) {
	case codes.InvalidArgument, codes.FailedPrecondition:
		code = 400
		typ = "invalid_request_error"
	case codes.Unauthenticated:
		code = 401
		typ = "authentication_error"
		w.Header().Set("WWW-Authenticate", "Bearer")
	case codes.PermissionDenied:
		code = 403
		typ = "permission_error"
	case codes.NotFound:
		code = 404
		typ = "invalid_request_error"
	case codes.ResourceExhausted:
		code = 413
		typ = "invalid_request_error"
	case codes.Unavailable:
		code = 503
	case codes.DeadlineExceeded:
		code = 504
	case codes.Canceled:
		code = 408
	}
	msg := cleanCode(e)
	if code == 500 {
		msg = "INTERNAL_ERROR"
	}
	jsonResponse(w, code, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": msg, "param": nil}})
}
