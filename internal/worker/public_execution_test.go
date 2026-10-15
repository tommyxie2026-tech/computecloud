package worker

import (
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

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
			if publicExecutionSupported("isolated_only", "codex_http", "0.160.1", tc.environment, tc.gateway, tc.capabilities, nil) {
				t.Fatal("unsafe execution path accepted")
			}
		})
	}
	if publicExecutionSupported("isolated_only", "codex_http", "0.160.1", "container", true, []string{"gateway_inference_v1"}, map[string]string{"ANTHROPIC_API_KEY": "local-secret"}) {
		t.Fatal("strict mode accepted operator runtime environment secrets")
	}
}

func TestPublicClaudeRequiresExactAnthropicGatewayContract(t *testing.T) {
	for _, tc := range []struct {
		name, profile, version string
		caps                   []string
		want                   bool
	}{
		{"supported", "claude_http", "2.1.292", []string{"gateway_anthropic_messages_v1"}, true},
		{"wrong protocol", "claude_http", "2.1.292", []string{"gateway_inference_v1"}, false},
		{"wrong version", "claude_http", "2.1.293", []string{"gateway_anthropic_messages_v1"}, false},
		{"codex protocol", "codex_http", "0.160.1", []string{"gateway_anthropic_messages_v1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := publicExecutionSupported("isolated_only", tc.profile, tc.version, "container", true, tc.caps, nil)
			if got != tc.want {
				t.Fatalf("supported=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestTrustedOnlyModePreservesExistingLocalBehavior(t *testing.T) {
	if !publicExecutionSupported("trusted_only", "claude_http", "any", "process", false, nil, map[string]string{"CUSTOM": "value"}) {
		t.Fatal("trusted mode changed existing local process behavior")
	}
}

func TestIsolatedRuntimeDoesNotInheritHostNetworkOverrides(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		t.Setenv(key, "http://proxy.invalid:3128")
	}
	env, _, err := runtimeEnv(config.Runtime{}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if strings.Contains(strings.ToLower(key), "_proxy") || key == "SSL_CERT_FILE" || key == "SSL_CERT_DIR" {
			t.Errorf("isolated runtime inherited host network override %s", key)
		}
	}
	trustedEnv, _, err := runtimeEnv(config.Runtime{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	foundTrustedProxy := false
	for _, item := range trustedEnv {
		foundTrustedProxy = foundTrustedProxy || strings.HasPrefix(item, "HTTP_PROXY=")
	}
	if !foundTrustedProxy {
		t.Fatal("trusted runtime no longer inherits its configured host proxy")
	}
}
