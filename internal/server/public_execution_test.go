package server

import (
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

func TestPublicStrictModeRejectsProcessEnvironment(t *testing.T) {
	s := &Server{cfg: config.Server{PublicExecutionMode: "isolated_only", ModelGateway: config.ModelGateway{Enabled: true, PublicBaseURL: "https://server.example/v1", CredentialRoutes: map[string]string{"cred": "route"}, Routes: map[string]config.ModelRoute{"route": {BaseURL: "https://provider.example/v1", MaxAttemptTokens: 1000, MaxAttemptCostMicros: 10000, InputCostPerMillionTokensMicros: 1000000, OutputCostPerMillionTokensMicros: 2000000}}}}}
	if _, ok := s.publicExecutionRequirements(&pb.TaskSpec{RuntimeProfile: "codex_http", CredentialRef: "cred", RequiredCapabilities: []string{"environment:process"}}); ok {
		t.Fatal("strict public mode accepted process environment")
	}
}

func TestPublicStrictModeRequiresAttemptGateway(t *testing.T) {
	s := &Server{cfg: config.Server{PublicExecutionMode: "isolated_only", ModelGateway: config.ModelGateway{Enabled: true, PublicBaseURL: "https://server.example/v1", Routes: map[string]config.ModelRoute{"route": {BaseURL: "https://provider.example/v1", MaxAttemptTokens: 1000, MaxAttemptCostMicros: 10000, InputCostPerMillionTokensMicros: 1000000, OutputCostPerMillionTokensMicros: 2000000}}}}}
	if _, ok := s.publicExecutionRequirements(&pb.TaskSpec{RuntimeProfile: "codex_http", CredentialRef: "cred", RequiredCapabilities: []string{"environment:container"}}); ok {
		t.Fatal("strict public mode accepted a direct credential route")
	}
	s.cfg.ModelGateway.CredentialRoutes = map[string]string{"cred": "route"}
	if _, ok := s.publicExecutionRequirements(&pb.TaskSpec{RuntimeProfile: "claude_http", CredentialRef: "cred", RequiredCapabilities: []string{"environment:container"}}); ok {
		t.Fatal("strict public mode accepted runtime without verified Gateway auth")
	}
	requirements, ok := s.publicExecutionRequirements(&pb.TaskSpec{RuntimeProfile: "codex_http", CredentialRef: "cred", RequiredCapabilities: []string{"environment:container"}})
	if !ok || len(requirements) != 2 || requirements[0] != "environment:container" || requirements[1] != "gateway_inference_v1" {
		t.Fatalf("strict requirements=%v eligible=%v", requirements, ok)
	}
}

func TestTrustedOnlyModePreservesExistingLocalBehavior(t *testing.T) {
	s := &Server{cfg: config.Server{PublicExecutionMode: "trusted_only"}}
	if requirements, ok := s.publicExecutionRequirements(&pb.TaskSpec{RequiredCapabilities: []string{"environment:process"}}); !ok || len(requirements) != 0 {
		t.Fatalf("trusted mode requirements=%v eligible=%v", requirements, ok)
	}
}

func TestPublicStrictModeFiltersNonContainerWorker(t *testing.T) {
	s := &Server{cfg: config.Server{PublicExecutionMode: "isolated_only", ModelGateway: config.ModelGateway{
		Enabled: true, PublicBaseURL: "https://server.example/v1", CredentialRoutes: map[string]string{"cred": "route"},
		Routes: map[string]config.ModelRoute{"route": {BaseURL: "https://provider.example/v1", MaxAttemptTokens: 1000, MaxAttemptCostMicros: 10000, InputCostPerMillionTokensMicros: 1000000, OutputCostPerMillionTokensMicros: 2000000}},
	}}}
	spec := &pb.TaskSpec{RuntimeProfile: "codex_http", ProjectId: "project", CredentialRef: "cred", Model: "model", PolicyRef: "policy", AcceptanceProfile: "accept", RequiredCapabilities: []string{"environment:container"}, Workspace: &pb.Workspace{RepositoryRef: "repo"}}
	requirements, ok := s.publicExecutionRequirements(spec)
	if !ok {
		t.Fatal("valid Codex Gateway container task rejected")
	}
	peer := &session{identity: config.Identity{Projects: []string{"project"}}, hello: &pb.WorkerHello{Runtimes: []*pb.Runtime{{
		Profile: "codex_http", Models: []string{"model"}, Credentials: []string{"cred"}, Repositories: []string{"repo"},
		Policies: []string{"policy"}, Verifiers: []string{"accept"}, Capabilities: []string{"environment:process", "gateway_inference_v1"},
	}}}}
	if fitsWithCapabilities(peer, &pb.Task{Spec: spec}, requirements) {
		t.Fatal("strict mode scheduled a process-only Worker")
	}
	peer.hello.Runtimes[0].Capabilities = []string{"environment:container", "gateway_inference_v1"}
	if !fitsWithCapabilities(peer, &pb.Task{Spec: spec}, requirements) {
		t.Fatal("strict mode rejected a compatible container/Gateway Worker")
	}
}
