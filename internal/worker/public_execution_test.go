package worker

import "testing"

func TestWorkerFailsClosedBeforeWorkspaceForUnsafeCredentialMode(t *testing.T) {
	for _, tc := range []struct {
		name, environment string
		gateway           bool
		capabilities      []string
	}{
		{name: "host process", environment: "process", gateway: true, capabilities: []string{"gateway_inference_v1"}},
		{name: "direct credential", environment: "container", capabilities: []string{"gateway_inference_v1"}},
		{name: "runtime cannot use gateway", environment: "container", gateway: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if publicExecutionSupported("isolated_only", tc.environment, tc.gateway, tc.capabilities, nil) {
				t.Fatal("unsafe execution path accepted")
			}
		})
	}
	if publicExecutionSupported("isolated_only", "container", true, []string{"gateway_inference_v1"}, map[string]string{"ANTHROPIC_API_KEY": "local-secret"}) {
		t.Fatal("strict mode accepted operator runtime environment secrets")
	}
}

func TestTrustedOnlyModePreservesExistingLocalBehavior(t *testing.T) {
	if !publicExecutionSupported("trusted_only", "process", false, nil, map[string]string{"CUSTOM": "value"}) {
		t.Fatal("trusted mode changed existing local process behavior")
	}
}
