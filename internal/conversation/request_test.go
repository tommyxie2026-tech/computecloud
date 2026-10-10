package conversation

import "testing"

func TestDecodeMessagesTextOnly(t *testing.T) {
	r, err := DecodeMessages([]byte(`{"model":"agent-default","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`), 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "agent-default" || len(r.Transcript) != 1 || r.Transcript[0].Text != "hello" {
		t.Fatalf("unexpected request: %#v", r)
	}
}

func TestDecodeResponsesRejectsToolOutput(t *testing.T) {
	if _, err := DecodeResponses([]byte(`{"model":"agent-default","input":[{"type":"function_call_output","output":"x"}]}`), 1024, 4096); err == nil {
		t.Fatal("expected executable/tool state to be rejected")
	}
}
