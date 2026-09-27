package adapter

import (
	"context"
	"reflect"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

type fixtureParser struct{}

func (fixtureParser) Line([]byte) error { return nil }
func (fixtureParser) Outcome() Outcome  { return Outcome{Final: true, Success: true} }

type fixtureProvider struct{ profile string }

func (f fixtureProvider) Profile() string { return f.profile }
func (f fixtureProvider) Probe(context.Context, config.Runtime) error { return nil }
func (f fixtureProvider) Args(*pb.TaskSpec, config.Policy) ([]string, error) {
	return []string{"fixture"}, nil
}
func (f fixtureProvider) Parser(func(string, []byte) error) StreamParser { return fixtureParser{} }
func (f fixtureProvider) Capabilities() CapabilitySet {
	return CapabilitySet{
		Runtime:     []string{"event_stream"},
		Tools:       []string{"shell"},
		Environment: []string{"process"},
	}
}
func (f fixtureProvider) SupportsGateway() bool { return false }

func TestRuntimeV2BuiltinsAndCapabilityNamespaces(t *testing.T) {
	wantProfiles := []string{"claude_print", "codex_exec"}
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
