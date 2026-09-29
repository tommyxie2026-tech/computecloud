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
