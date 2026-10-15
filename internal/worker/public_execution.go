package worker

import "github.com/tommyxie2026-tech/computecloud/internal/config"

func publicExecutionSupported(mode, profile, version, environment string, attemptGateway bool, runtimeCapabilities []string, runtimeEnv map[string]string) bool {
	if mode != "isolated_only" {
		return true
	}
	if environment != "container" || !attemptGateway || len(runtimeEnv) != 0 {
		return false
	}
	switch profile {
	case "codex_http":
		return config.Contains(runtimeCapabilities, "gateway_inference_v1")
	case "claude_http":
		return version == "2.1.292" && config.Contains(runtimeCapabilities, "gateway_anthropic_messages_v1")
	default:
		return false
	}
}
