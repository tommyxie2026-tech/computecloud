package worker

import "github.com/tommyxie2026-tech/computecloud/internal/config"

func publicExecutionSupported(mode, environment string, attemptGateway bool, runtimeCapabilities []string) bool {
	if mode != "isolated_only" {
		return true
	}
	return environment == "container" && attemptGateway && config.Contains(runtimeCapabilities, "gateway_inference_v1")
}
