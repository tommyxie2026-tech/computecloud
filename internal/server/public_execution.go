package server

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/environment"
)

// publicExecutionRequirements returns the capability fence for an untrusted
// public job. It intentionally supports only the existing Attempt gateway
// mapping and container environment contract.
func (s *Server) publicExecutionRequirements(spec *pb.TaskSpec) ([]string, bool) {
	if s.cfg.PublicExecutionMode != "isolated_only" {
		return nil, true
	}
	if spec == nil || environment.RequiredName(spec.RequiredCapabilities) != "container" ||
		!s.cfg.ModelGateway.Enabled || s.cfg.ModelGateway.PublicBaseURL == "" ||
		s.cfg.ModelGateway.CredentialRoutes[spec.CredentialRef] == "" {
		return nil, false
	}
	route := s.cfg.ModelGateway.Routes[s.cfg.ModelGateway.CredentialRoutes[spec.CredentialRef]]
	if route.BaseURL == "" || route.Backend == "codex_cli" || !config.Contains(route.AllowedModels, spec.Model) || route.MaxAttemptTokens < 1 || route.MaxAttemptCostMicros < 1 || route.InputCostPerMillionTokensMicros < 1 || route.OutputCostPerMillionTokensMicros < 1 {
		return nil, false
	}
	switch {
	case spec.RuntimeProfile == "codex_http" && route.Backend != "anthropic_messages":
		return []string{"environment:container", "gateway_inference_v1"}, true
	case spec.RuntimeProfile == "claude_http" && route.Backend == "anthropic_messages":
		return []string{"environment:container", "gateway_anthropic_messages_v1"}, true
	default:
		return nil, false
	}
}

func publicExecutionMode(c config.Server) bool { return c.PublicExecutionMode == "isolated_only" }
