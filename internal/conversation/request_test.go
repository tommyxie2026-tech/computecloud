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
