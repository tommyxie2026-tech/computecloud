package adapter

import (
	"context"
	"reflect"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

type fixtureParser struct{}

func (fixtureParser) Line([]byte) error { return nil }
func (fixtureParser) Outcome() Outcome  { return Outcome{Final: true, Success: true} }

type fixtureProvider struct{ profile string }

func (f fixtureProvider) Profile() string { return f.profile }
func (f fixtureProvider) Version(r config.Runtime) string { return r.Version }
func (f fixtureProvider) Transport() string { return "remote_api" }
func (f fixtureProvider) Probe(context.Context, config.Runtime) error { return nil }
func (f fixtureProvider) Args(*pb.TaskSpec, config.Policy) ([]string, error) {
	return []string{"fixture"}, nil
}
func (f fixtureProvider) Parser(func(string, []byte) error) StreamParser { return fixtureParser{} }
func (f fixtureProvider) Prepare(req PrepareRequest) (PreparedExecution, error) {
	return PreparedExecution{Profile: f.profile, Runtime: req.Runtime, CWD: req.CWD, Input: req.Input, Emit: req.Emit, Stderr: req.Stderr, StopGrace: req.StopGrace}, nil
}
func (f fixtureProvider) Start(_ context.Context, _ PreparedExecution, started func(ExecutionRef) error) StartResult {
	ref := ExecutionRef{Provider: f.profile, Transport: "remote_api", ID: "fixture-ref"}
	if started != nil {
		if err := started(ref); err != nil {
			return StartResult{Ref: ref, State: RuntimeUnknown, Cleanup: CleanupUnknown, Err: err}
		}
	}
	return StartResult{Ref: ref, State: RuntimeExited, Cleanup: CleanupConfirmed, ExitCode: 0, Outcome: Outcome{Final: true, Success: true}}
}
func (f fixtureProvider) Inspect(context.Context, config.Runtime, ExecutionRef) (Inspection, error) {
	return Inspection{State: RuntimeExited, Cleanup: CleanupConfirmed}, nil
}
func (f fixtureProvider) Stop(context.Context, config.Runtime, ExecutionRef, time.Duration) (StopResult, error) {
	return StopResult{State: RuntimeExited, Cleanup: CleanupConfirmed}, nil
}
func (f fixtureProvider) Capabilities() CapabilitySet {
	return CapabilitySet{
		Runtime:     []string{"event_stream", "remote_api"},
		Tools:       []string{"shell"},
		Environment: []string{"process"},
	}
}
func (f fixtureProvider) SupportsGateway() bool { return false }

func TestRuntimeV2BuiltinsAndCapabilityNamespaces(t *testing.T) {
	wantProfiles := []string{"claude_print", "codex_exec", "gemini_cli"}
	got := Profiles()
	if !reflect.DeepEqual(got, wantProfiles) {
		t.Fatalf("profiles=%v want=%v", got, wantProfiles)
	}
	codex, ok := Lookup("codex_exec")
	if !ok {
		t.Fatal("codex provider missing")
	}
	if !codex.SupportsGateway() {
		t.Fatal("codex provider lost gateway capability")
	}
	claude, ok := Lookup("claude_print")
	if !ok {
		t.Fatal("claude provider missing")
	}
	if claude.SupportsGateway() {
		t.Fatal("claude unexpectedly supports gateway")
	}
	caps := codex.Capabilities().Advertised()
	for _, required := range []string{
		"runtime:event_stream",
		"runtime:cancel",
		"runtime:gateway_inference_v1",
		"runtime:local_cli",
		"tool:job_io_v1",
		"tool:artifact_inputs_v1",
		"environment:process",
		"event_stream",
		"gateway_inference_v1",
	} {
		found := false
		for _, got := range caps {
			found = found || got == required
		}
		if !found {
			t.Fatalf("missing capability %q in %v", required, caps)
		}
	}
	if !reflect.DeepEqual(caps, codex.Capabilities().Advertised()) {
		t.Fatal("capability advertisement is not deterministic")
	}
}

func TestRuntimeV2RegistryAllowsProviderWithoutCoreChanges(t *testing.T) {
	const profile = "fixture_runtime_v2"
	if err := Register(fixtureProvider{profile: profile}); err != nil {
		t.Fatal(err)
	}
	p, ok := Lookup(profile)
	if !ok {
		t.Fatal("registered provider missing")
	}
	args, err := p.Args(&pb.TaskSpec{RuntimeProfile: profile}, config.Policy{})
	if err != nil || !reflect.DeepEqual(args, []string{"fixture"}) {
		t.Fatalf("args=%v err=%v", args, err)
	}
	caps := p.Capabilities().Advertised()
	for _, want := range []string{"runtime:event_stream", "tool:shell", "environment:process"} {
		found := false
		for _, got := range caps {
			found = found || got == want
		}
		if !found {
			t.Fatalf("missing %q in %v", want, caps)
		}
	}
	if err := Register(fixtureProvider{profile: profile}); err == nil {
		t.Fatal("duplicate provider registration accepted")
	}
	if _, err := NewParser("missing-runtime", nil); err == nil {
		t.Fatal("unknown runtime parser accepted")
	}
	if _, ok := Lookup("missing-runtime"); ok {
		t.Fatal("unknown runtime lookup succeeded")
	}
}

func TestRuntimeV2CompatibilityArgs(t *testing.T) {
	for _, tc := range []struct {
		profile string
		policy  config.Policy
	}{
		{"codex_exec", config.Policy{CodexSandbox: "read-only"}},
		{"claude_print", config.Policy{ClaudePermissionMode: "dontAsk"}},
		{"gemini_cli", config.Policy{GeminiApprovalMode: "plan"}},
	} {
		spec := &pb.TaskSpec{RuntimeProfile: tc.profile, Model: "fixture-model"}
		legacy, err := Args(spec, tc.policy)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := Lookup(tc.profile)
		v2, err := p.Args(spec, tc.policy)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(legacy, v2) {
			t.Fatalf("%s wrapper drift legacy=%v v2=%v", tc.profile, legacy, v2)
		}
	}
}


type invalidCapabilityProvider struct{}

func (invalidCapabilityProvider) Profile() string { return "invalid_capability_fixture" }
func (invalidCapabilityProvider) Version(r config.Runtime) string { return r.Version }
func (invalidCapabilityProvider) Transport() string { return "remote_api" }
func (invalidCapabilityProvider) Probe(context.Context, config.Runtime) error { return nil }
func (invalidCapabilityProvider) Args(*pb.TaskSpec, config.Policy) ([]string, error) { return nil, nil }
func (invalidCapabilityProvider) Parser(func(string, []byte) error) StreamParser { return fixtureParser{} }
func (invalidCapabilityProvider) Prepare(req PrepareRequest) (PreparedExecution, error) { return PreparedExecution{Profile: "invalid_capability_fixture", Runtime: req.Runtime}, nil }
func (invalidCapabilityProvider) Start(context.Context, PreparedExecution, func(ExecutionRef) error) StartResult { return StartResult{} }
func (invalidCapabilityProvider) Inspect(context.Context, config.Runtime, ExecutionRef) (Inspection, error) { return Inspection{State: RuntimeUnknown, Cleanup: CleanupUnknown}, nil }
func (invalidCapabilityProvider) Stop(context.Context, config.Runtime, ExecutionRef, time.Duration) (StopResult, error) { return StopResult{State: RuntimeUnknown, Cleanup: CleanupUnknown}, nil }
func (invalidCapabilityProvider) Capabilities() CapabilitySet {
	return CapabilitySet{Runtime: []string{"runtime:already-qualified"}}
}
func (invalidCapabilityProvider) SupportsGateway() bool { return false }

func TestRuntimeV2RejectsMalformedCapabilityNamespace(t *testing.T) {
	if err := Register(invalidCapabilityProvider{}); err == nil {
		t.Fatal("provider with pre-qualified runtime capability was accepted")
	}
}


func TestRuntimeExecutionRefRoundTrip(t *testing.T) {
	ref := ExecutionRef{Provider: "fixture", Transport: "remote_api", ID: "run-123"}
	b, err := EncodeExecutionRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeExecutionRef(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, ref) {
		t.Fatalf("ref=%+v want=%+v", got, ref)
	}
	if _, err = DecodeExecutionRef([]byte(`{"provider":"","transport":"remote_api","id":"x"}`)); err == nil {
		t.Fatal("incomplete runtime ref accepted")
	}
}

func TestRemoteFixtureExecutionContractUsesNoLocalPID(t *testing.T) {
	p := fixtureProvider{profile: "transport_fixture"}
	prepared, err := p.Prepare(PrepareRequest{Runtime: config.Runtime{Version: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	var persisted ExecutionRef
	result := p.Start(context.Background(), prepared, func(ref ExecutionRef) error {
		persisted = ref
		return nil
	})
	if result.Err != nil || !result.Outcome.Final || !result.Outcome.Success {
		t.Fatalf("remote start failed: %+v", result)
	}
	if persisted.Transport != "remote_api" || persisted.PID != 0 || persisted.StartID != "" {
		t.Fatalf("remote provider leaked local process identity: %+v", persisted)
	}
	inspection, err := p.Inspect(context.Background(), config.Runtime{}, persisted)
	if err != nil || inspection.State != RuntimeExited || inspection.Cleanup != CleanupConfirmed {
		t.Fatalf("remote inspect=%+v err=%v", inspection, err)
	}
}


func TestGeminiRuntimeV2Contract(t *testing.T) {
	p, ok := Lookup("gemini_cli")
	if !ok {
		t.Fatal("gemini provider missing")
	}
	if p.SupportsGateway() {
		t.Fatal("gemini unexpectedly supports model gateway")
	}
	spec := &pb.TaskSpec{RuntimeProfile: "gemini_cli", Model: "gemini-fixture"}
	args, err := p.Args(spec, config.Policy{GeminiApprovalMode: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--output-format", "stream-json", "--approval-mode", "plan", "--skip-trust", "--model", "gemini-fixture"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("gemini args=%v want=%v", args, want)
	}
	if _, err := p.Args(spec, config.Policy{}); err == nil {
		t.Fatal("gemini accepted implicit approval policy")
	}
}
