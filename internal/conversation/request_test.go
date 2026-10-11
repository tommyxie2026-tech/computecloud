package conversation

import (
	"errors"
	"testing"
)

func TestDecodeMessagesTextOnly(t *testing.T) {
	r, err := DecodeMessages([]byte(`{"model":"agent-default","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`), 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "agent-default" || len(r.Transcript) != 1 || r.Transcript[0].Text != "hello" {
		t.Fatalf("unexpected request: %#v", r)
	}
}

func TestDecodeClaudeCodeMessagesWithMetadataAndSystemMessage(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash","max_tokens":32000,"stream":true,"context_management":{"edits":[]},"output_config":{"effort":"max"},"thinking":{"type":"adaptive"},"system":[{"type":"text","text":"client system instructions"}],"messages":[{"role":"user","content":"hello"},{"role":"system","content":"additional system context"}]}`)
	r, err := DecodeMessages(body, 256<<10, 4096)
	if err != nil {
		t.Fatalf("Claude Code text request should be accepted: %v", err)
	}
	if r.Model != "deepseek-flash" || !r.Stream || r.MaxOutputTokens != 4096 || len(r.Transcript) != 3 {
		t.Fatalf("unexpected request: %#v", r)
	}
	if r.Transcript[0].Role != "system" || r.Transcript[0].Text != "client system instructions" {
		t.Fatalf("system field was not preserved: %#v", r.Transcript[0])
	}
	if r.Transcript[1].Role != "user" || r.Transcript[1].Text != "hello" {
		t.Fatalf("user message was not preserved: %#v", r.Transcript[1])
	}
	if r.Transcript[2].Role != "system" || r.Transcript[2].Text != "additional system context" {
		t.Fatalf("system message was not preserved: %#v", r.Transcript[2])
	}
}

func TestDecodeResponsesTextAndRejectDuplicateKeys(t *testing.T) {
	r, err := DecodeResponses([]byte(`{"model":"agent-default","instructions":"be brief","input":[{"role":"user","content":"hello"}],"max_output_tokens":100}`), 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if r.Protocol != "responses" || len(r.Transcript) != 2 || r.Transcript[0].Role != "system" || r.Transcript[1].Text != "hello" {
		t.Fatalf("unexpected request: %#v", r)
	}
	if _, err = DecodeResponses([]byte(`{"model":"agent-default","model":"other","input":"hello","max_output_tokens":100}`), 1024, 4096); err == nil {
		t.Fatal("expected duplicate JSON member to be rejected")
	}
}

func TestDecodeResponsesClampsOutputTokensToProfile(t *testing.T) {
	r, err := DecodeResponses([]byte(`{"model":"agent-default","input":"hello","max_output_tokens":32000}`), 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if r.MaxOutputTokens != 4096 {
		t.Fatalf("output token cap should be clamped to profile: got %d", r.MaxOutputTokens)
	}
}

func TestDecodeMessagesRejectsForcedTools(t *testing.T) {
	if _, err := DecodeMessages([]byte(`{"model":"agent-default","max_tokens":100,"tool_choice":{"type":"tool","name":"shell"},"messages":[{"role":"user","content":"run"}]}`), 1024, 4096); err == nil {
		t.Fatal("expected forced tool choice to be rejected")
	}
}

func TestDecodeMessagesAllowsAutomaticToolMetadataOnly(t *testing.T) {
	if _, err := DecodeMessages([]byte(`{"model":"agent-default","max_tokens":100,"tools":[{"name":"shell","input_schema":{"type":"object"}}],"tool_choice":"auto","messages":[{"role":"user","content":"hello"}]}`), 1024, 4096); err != nil {
		t.Fatalf("automatic tool metadata should remain inert: %v", err)
	}
}

func TestUnsupportedMediaAndToolLoopHaveStableCapabilityError(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		body     string
	}{
		{"media", "messages", `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`},
		{"tool loop", "responses", `{"model":"m","input":[{"type":"function_call_output","call_id":"x","output":"result"}],"max_output_tokens":10}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.protocol == "messages" {
				_, err = DecodeMessages([]byte(tc.body), 1024, 100)
			} else {
				_, err = DecodeResponses([]byte(tc.body), 1024, 100)
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("expected stable capability error, got %v", err)
			}
		})
	}
}

func TestDecodeResponsesRejectsToolOutput(t *testing.T) {
	if _, err := DecodeResponses([]byte(`{"model":"agent-default","input":[{"type":"function_call_output","output":"x"}]}`), 1024, 4096); err == nil {
		t.Fatal("expected executable/tool state to be rejected")
	}
}
