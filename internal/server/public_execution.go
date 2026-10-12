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
	if spec == nil || spec.RuntimeProfile != "codex_http" || environment.RequiredName(spec.RequiredCapabilities) != "container" ||
		!s.cfg.ModelGateway.Enabled || s.cfg.ModelGateway.PublicBaseURL == "" ||
		s.cfg.ModelGateway.CredentialRoutes[spec.CredentialRef] == "" {
		return nil, false
	}
	route := s.cfg.ModelGateway.Routes[s.cfg.ModelGateway.CredentialRoutes[spec.CredentialRef]]
	if route.BaseURL == "" || route.Backend == "codex_cli" || route.MaxAttemptTokens < 1 {
		return nil, false
	}
	return []string{"environment:container", "gateway_inference_v1"}, true
}

func publicExecutionMode(c config.Server) bool { return c.PublicExecutionMode == "isolated_only" }
