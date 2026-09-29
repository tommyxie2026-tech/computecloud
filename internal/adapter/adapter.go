package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

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

type StreamParser interface {
	Line([]byte) error
	Outcome() Outcome
}

type CapabilitySet struct {
	Runtime     []string
	Tools       []string
	Environment []string
	Legacy      []string
}

func namespaced(prefix string, values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, prefix+":"+v)
		}
	}
	return out
}

func (c CapabilitySet) Advertised() []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range [][]string{
		namespaced("runtime", c.Runtime),
		namespaced("tool", c.Tools),
		namespaced("environment", c.Environment),
		c.Legacy,
	} {
		for _, v := range group {
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

type Provider interface {
	Profile() string
	Version(config.Runtime) string
	Transport() string
	Probe(context.Context, config.Runtime) error
	Args(*pb.TaskSpec, config.Policy) ([]string, error)
	Parser(func(string, []byte) error) StreamParser
	Prepare(PrepareRequest) (PreparedExecution, error)
	Start(context.Context, PreparedExecution, func(ExecutionRef) error) StartResult
	Inspect(context.Context, config.Runtime, ExecutionRef) (Inspection, error)
	Stop(context.Context, config.Runtime, ExecutionRef, time.Duration) (StopResult, error)
	Capabilities() CapabilitySet
	SupportsGateway() bool
}

var registry = struct {
	sync.RWMutex
	providers map[string]Provider
}{providers: map[string]Provider{}}

func validateCapabilitySet(c CapabilitySet) error {
	for namespace, values := range map[string][]string{
		"runtime": c.Runtime,
		"tool": c.Tools,
		"environment": c.Environment,
	} {
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || strings.Contains(value, ":") {
				return fmt.Errorf("%s capability must be an unqualified non-empty name: %q", namespace, value)
			}
			if seen[value] {
				return fmt.Errorf("duplicate %s capability: %s", namespace, value)
			}
			seen[value] = true
		}
	}
	return nil
}

func Register(p Provider) error {
	if p == nil || strings.TrimSpace(p.Profile()) == "" {
		return errors.New("runtime provider profile required")
	}
	profile := strings.TrimSpace(p.Profile())
	if strings.Contains(profile, ":") {
		return errors.New("runtime provider profile must not contain ':'")
	}
	if err := validateCapabilitySet(p.Capabilities()); err != nil {
		return err
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.providers[profile]; exists {
		return fmt.Errorf("runtime provider already registered: %s", profile)
	}
	registry.providers[profile] = p
	return nil
}

func Lookup(profile string) (Provider, bool) {
	registry.RLock()
	defer registry.RUnlock()
	p, ok := registry.providers[profile]
	return p, ok
}

func Profiles() []string {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]string, 0, len(registry.providers))
	for profile := range registry.providers {
		out = append(out, profile)
	}
	sort.Strings(out)
	return out
}

func probeCLI(ctx context.Context, r config.Runtime) error {
	if r.Executable == "" || r.Version == "" {
		return errors.New("runtime executable/version required")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, r.Executable, "--version")
	cmd.WaitDelay = time.Second
	b, err := cmd.Output()
	if err != nil {
		return err
	}
	observed := strings.TrimSpace(string(b))
	if observed != r.Version {
		return fmt.Errorf("version mismatch: configured %q, observed %q", r.Version, observed)
	}
	return nil
}

type codexProvider struct{}

func (codexProvider) Profile() string { return "codex_exec" }
func (codexProvider) Probe(ctx context.Context, r config.Runtime) error { return probeCLI(ctx, r) }
func (codexProvider) Args(spec *pb.TaskSpec, policy config.Policy) ([]string, error) {
	if policy.CodexSandbox != "read-only" && policy.CodexSandbox != "workspace-write" {
		return nil, errors.New("policy must set read-only or workspace-write sandbox")
	}
	return []string{"exec", "--json", "--sandbox", policy.CodexSandbox, "--model", spec.Model, "-"}, nil
}
func (codexProvider) Parser(emit func(string, []byte) error) StreamParser {
	return &Parser{Profile: "codex_exec", Emit: emit}
}
func (codexProvider) Capabilities() CapabilitySet {
	return CapabilitySet{
		Runtime:     []string{"event_stream", "cancel", "gateway_inference_v1", "local_cli"},
		Tools:       []string{"job_io_v1", "artifact_inputs_v1"},
		Environment: []string{"process"},
		Legacy:      []string{"event_stream", "cancel", "gateway_inference_v1", "job_io_v1", "artifact_inputs_v1"},
	}
}
func (codexProvider) SupportsGateway() bool { return true }

type claudeProvider struct{}

func (claudeProvider) Profile() string { return "claude_print" }
func (claudeProvider) Probe(ctx context.Context, r config.Runtime) error { return probeCLI(ctx, r) }
func (claudeProvider) Args(spec *pb.TaskSpec, policy config.Policy) ([]string, error) {
	if policy.ClaudePermissionMode != "dontAsk" {
		return nil, errors.New("batch policy requires claude_permission_mode: dontAsk")
	}
	args := []string{"-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-mode", "dontAsk", "--model", spec.Model}
	if len(policy.ClaudeAllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(policy.ClaudeAllowedTools, ","))
	}
	return args, nil
}
func (claudeProvider) Parser(emit func(string, []byte) error) StreamParser {
	return &Parser{Profile: "claude_print", Emit: emit}
}
func (claudeProvider) Capabilities() CapabilitySet {
	return CapabilitySet{
		Runtime:     []string{"event_stream", "cancel", "local_cli"},
		Tools:       []string{"job_io_v1", "artifact_inputs_v1"},
		Environment: []string{"process"},
		Legacy:      []string{"event_stream", "cancel", "job_io_v1", "artifact_inputs_v1"},
	}
}
func (claudeProvider) SupportsGateway() bool { return false }

func init() {
	for _, p := range []Provider{codexProvider{}, claudeProvider{}} {
		if err := Register(p); err != nil {
			panic(err)
		}
	}
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
	payload := line
	sessionStarted := func(session string) {
		if session == "" {
			return
		}
		p.outcome.Session = session
		typ = "session.started"
		payload, _ = json.Marshal(map[string]string{"session_ref": session})
	}
	if p.Profile == "codex_exec" {
		switch kind {
		case "thread.started":
			sessionStarted(str("thread_id"))
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
			sessionStarted(str("session_id"))
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
	if p.Emit == nil {
		return nil
	}
	return p.Emit(typ, payload)
}

func Args(spec *pb.TaskSpec, policy config.Policy) ([]string, error) {
	p, ok := Lookup(spec.RuntimeProfile)
	if !ok {
		return nil, errors.New("unsupported runtime profile")
	}
	return p.Args(spec, policy)
}

func NewParser(profile string, emit func(string, []byte) error) (StreamParser, error) {
	p, ok := Lookup(profile)
	if !ok {
		return nil, errors.New("unsupported runtime profile")
	}
	return p.Parser(emit), nil
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
