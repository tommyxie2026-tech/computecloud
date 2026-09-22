package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

type Outcome struct {
	Final   bool
	Success bool
	Result  string
	Session string
	Code    string
}
type Parser struct {
	Profile string
	outcome Outcome
	Emit    func(string, []byte) error
}

func (p *Parser) Outcome() Outcome { return p.outcome }
func (p *Parser) Line(line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if e := json.Unmarshal(line, &m); e != nil {
		return fmt.Errorf("PROTOCOL_ERROR: %w", e)
	}
	str := func(k string) string { var s string; _ = json.Unmarshal(m[k], &s); return s }
	kind := str("type")
	typ := "runtime.diagnostic"
	if p.Profile == "codex_exec" {
		switch kind {
		case "thread.started":
			p.outcome.Session = str("thread_id")
		case "turn.completed":
			p.outcome.Final = true
			p.outcome.Success = p.outcome.Code == ""
			typ = "usage.updated"
		case "turn.failed", "error":
			p.outcome.Final = true
			p.outcome.Success = false
			p.outcome.Code = "RUNTIME_FAILED"
		case "item.completed":
			var item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(m["item"], &item)
			if item.Type == "agent_message" {
				p.outcome.Result = item.Text
				typ = "message.completed"
			} else {
				typ = "tool.completed"
			}
		case "item.started":
			typ = "tool.started"
		}
	} else {
		switch kind {
		case "system":
			if s := str("session_id"); s != "" {
				p.outcome.Session = s
			}
		case "assistant":
			typ = "message.completed"
		case "stream_event":
			typ = "message.delta"
		case "result":
			p.outcome.Final = true
			p.outcome.Result = str("result")
			p.outcome.Session = str("session_id")
			var bad bool
			_ = json.Unmarshal(m["is_error"], &bad)
			p.outcome.Success = !bad && str("subtype") == "success"
			if !p.outcome.Success {
				p.outcome.Code = "RUNTIME_FAILED"
			}
			typ = "runtime.result"
		}
	}
	if len(p.outcome.Result) > 1<<20 {
		return errors.New("PROTOCOL_LIMIT: result exceeds 1 MiB")
	}
	return p.Emit(typ, line)
}
func Args(spec *pb.TaskSpec, policy config.Policy) ([]string, error) {
	switch spec.RuntimeProfile {
	case "codex_exec":
		if policy.CodexSandbox != "read-only" && policy.CodexSandbox != "workspace-write" {
			return nil, errors.New("policy must set read-only or workspace-write sandbox")
		}
		return []string{"exec", "--json", "--sandbox", policy.CodexSandbox, "--model", spec.Model, "-"}, nil
	case "claude_print":
		if policy.ClaudePermissionMode != "dontAsk" {
			return nil, errors.New("batch policy requires claude_permission_mode: dontAsk")
		}
		a := []string{"-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-mode", "dontAsk", "--model", spec.Model}
		if len(policy.ClaudeAllowedTools) > 0 {
			a = append(a, "--allowedTools", strings.Join(policy.ClaudeAllowedTools, ","))
		}
		return a, nil
	default:
		return nil, errors.New("unsupported runtime profile")
	}
}

// Lines bounds an individual protocol frame and propagates errors to os/exec.
type Lines struct {
	mu      sync.Mutex
	buf     []byte
	Limit   int
	OnLine  func([]byte) error
	Err     error
	OnError func()
}

func (l *Lines) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Err != nil {
		return 0, l.Err
	}
	total := len(b)
	for len(b) > 0 {
		at := bytes.IndexByte(b, '\n')
		n := len(b)
		if at >= 0 {
			n = at
		}
		if len(l.buf)+n > l.Limit {
			return 0, l.fail(errors.New("PROTOCOL_LIMIT: frame too large"))
		}
		l.buf = append(l.buf, b[:n]...)
		if at < 0 {
			break
		}
		if e := l.OnLine(l.buf); e != nil {
			return 0, l.fail(e)
		}
		l.buf = nil
		b = b[n+1:]
	}
	return total, nil
}
func (l *Lines) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Err != nil {
		return l.Err
	}
	if len(l.buf) > 0 {
		l.Err = l.OnLine(l.buf)
		l.buf = nil
	}
	return l.Err
}
func (l *Lines) fail(e error) error {
	l.Err = e
	if l.OnError != nil {
		l.OnError()
	}
	return e
}
