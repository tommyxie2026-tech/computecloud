package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeFinalAndProtocolBounds(t *testing.T) {
	for _, tc := range []struct {
		profile, input string
		ok, final      bool
	}{
		{"codex_exec", `{"type":"turn.completed","usage":{}}`, true, true},
		{"codex_exec", `{"type":"turn.failed","error":{"message":"failure"}}`, false, true},
		{"codex_exec", `{"type":"item.completed","item":{"type":"agent_message","text":"claims success"}}`, false, false},
		{"claude_print", `{"type":"result","subtype":"success","is_error":false,"result":"done"}`, true, true},
		{"claude_print", `{"type":"result","subtype":"error_during_execution","is_error":true}`, false, true},
	} {
		p := Parser{Profile: tc.profile, Emit: func(string, []byte) error { return nil }}
		if e := p.Line([]byte(tc.input)); e != nil {
			t.Fatal(e)
		}
		out := p.Outcome()
		if out.Success != tc.ok || out.Final != tc.final {
			t.Fatalf("%s: %+v", tc.input, out)
		}
	}
	l := Lines{Limit: 16, OnLine: func([]byte) error { return nil }}
	if _, e := l.Write([]byte(strings.Repeat("x", 17))); e == nil {
		t.Fatal("frame bound ignored")
	}
	p := Parser{Profile: "codex_exec", Emit: func(string, []byte) error { return nil }}
	if e := p.Line([]byte("not JSON")); e == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestNativeUsageAggregationAndUnknown(t *testing.T) {
	p := Parser{Profile: "codex_exec", Emit: func(string, []byte) error { return nil }}
	for _, raw := range []string{`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2,"cached_input_tokens":1}}`, `{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":4}}`} {
		if err := p.Line([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	out := p.Outcome()
	if !out.UsageComplete || out.Usage.Input != 8 || out.Usage.Output != 6 || out.Usage.CachedInput != 1 {
		t.Fatalf("%+v", out)
	}
	if err := p.Line([]byte(`{"type":"turn.completed","usage":{}}`)); err != nil {
		t.Fatal(err)
	}
	if p.Outcome().UsageComplete {
		t.Fatal("unknown usage marked complete")
	}
	c := Parser{Profile: "claude_print", Emit: func(string, []byte) error { return nil }}
	if err := c.Line([]byte(`{"type":"result","subtype":"success","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":5,"cache_creation_input_tokens":7}}`)); err != nil {
		t.Fatal(err)
	}
	if !c.Outcome().UsageComplete || c.Outcome().Usage.Input != 15 {
		t.Fatalf("%+v", c.Outcome())
	}
}

func TestRuntimeParserEmitsNormalizedSessionStarted(t *testing.T) {
	for _, tc := range []struct {
		profile string
		input   string
		want    string
	}{
		{"codex_exec", `{"type":"thread.started","thread_id":"codex-session-1"}`, "codex-session-1"},
		{"claude_print", `{"type":"system","session_id":"claude-session-1"}`, "claude-session-1"},
	} {
		var gotType string
		var gotPayload []byte
		p := Parser{Profile: tc.profile, Emit: func(kind string, payload []byte) error {
			gotType = kind
			gotPayload = append([]byte(nil), payload...)
			return nil
		}}
		if err := p.Line([]byte(tc.input)); err != nil {
			t.Fatal(err)
		}
		if gotType != "session.started" {
			t.Fatalf("%s event type=%s", tc.profile, gotType)
		}
		var payload struct {
			SessionRef string `json:"session_ref"`
		}
		if err := json.Unmarshal(gotPayload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.SessionRef != tc.want || p.Outcome().Session != tc.want {
			t.Fatalf("%s payload=%+v outcome=%+v", tc.profile, payload, p.Outcome())
		}
	}
}

func TestGeminiStreamJSONParser(t *testing.T) {
	var types []string
	p := Parser{Profile: "gemini_cli", Emit: func(kind string, _ []byte) error {
		types = append(types, kind)
		return nil
	}}
	for _, line := range []string{
		`{"type":"init","timestamp":"2026-09-29T00:00:00Z","session_id":"gem-session","model":"gemini-fixture"}`,
		`{"type":"message","timestamp":"2026-09-29T00:00:01Z","role":"assistant","content":"hello ","delta":true}`,
		`{"type":"tool_use","timestamp":"2026-09-29T00:00:02Z","tool_name":"read_file","tool_id":"tool-1","parameters":{}}`,
		`{"type":"tool_result","timestamp":"2026-09-29T00:00:03Z","tool_id":"tool-1","status":"success","output":"ok"}`,
		`{"type":"message","timestamp":"2026-09-29T00:00:04Z","role":"assistant","content":"world","delta":true}`,
		`{"type":"result","timestamp":"2026-09-29T00:00:05Z","status":"success","stats":{}}`,
	} {
		if err := p.Line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	out := p.Outcome()
	if !out.Final || !out.Success || out.Session != "gem-session" || out.Result != "hello world" {
		t.Fatalf("unexpected gemini outcome: %+v", out)
	}
	want := []string{"session.started", "message.delta", "tool.started", "tool.completed", "message.delta", "runtime.result"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event types=%v want=%v", types, want)
	}
}
