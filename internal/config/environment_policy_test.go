package config

import "testing"

func TestAllowedEnvironmentsAffectTemplateDigest(t *testing.T) {
	base := Policy{CodexSandbox: "read-only"}
	withEnvironment := Policy{CodexSandbox: "read-only", AllowedEnvironments: []string{"process"}}
	a := TemplateDigest("runtime-v1", base, nil)
	b := TemplateDigest("runtime-v1", withEnvironment, nil)
	if a == b {
		t.Fatal("environment policy did not alter template digest")
	}
}

func TestWorkerPolicyEnvironmentNameValidation(t *testing.T) {
	base := Worker{
		ID: "w", Address: "127.0.0.1:1", DataDir: t.TempDir(), Slots: 1,
		Runtimes:     map[string]Runtime{"codex_exec": {Version: "v"}},
		Repositories: map[string]string{"repo": t.TempDir()},
	}
	for _, environments := range [][]string{{"Bad"}, {"environment:qualified"}, {"process", "process"}} {
		w := base
		w.Policies = map[string]Policy{"p": {AllowedEnvironments: environments}}
		if err := w.Validate(); err == nil {
			t.Fatalf("invalid policy environments accepted: %v", environments)
		}
	}
}

func TestPublicExecutionModeValidation(t *testing.T) {
	base := Server{DataDir: "/tmp/server", LeaseSeconds: 60, TickMS: 500, MaxArtifactBytes: 1024, MaxProjectTasks: 1}
	for _, mode := range []string{"", "trusted_only"} {
		base.PublicExecutionMode = mode
		if err := base.Validate(); err != nil {
			t.Fatalf("valid mode %q rejected: %v", mode, err)
		}
	}
	for _, mode := range []string{"unsafe", "public", "disabled"} {
		base.PublicExecutionMode = mode
		if err := base.Validate(); err == nil || err.Error() != "public_execution_mode must be trusted_only or isolated_only" {
			t.Fatalf("invalid mode %q: got %v", mode, err)
		}
	}
}

func TestIsolatedPublicModeRequiresBoundedHTTPSGatewayRoute(t *testing.T) {
	base := Server{
		DataDir: "/tmp/server", LeaseSeconds: 60, TickMS: 500, MaxArtifactBytes: 1024, MaxProjectTasks: 1,
		PublicExecutionMode: "isolated_only", Credentials: map[string]int{"cred": 1},
		ModelGateway: ModelGateway{
			Enabled: true, PublicBaseURL: "https://server.example/v1", MaxRequestBytes: 1 << 20, MaxInflight: 4,
			ConnectTimeoutSeconds: 5, HeaderTimeoutSeconds: 5, StreamIdleTimeoutSeconds: 5, RequestTimeoutSeconds: 60,
			Routes:           map[string]ModelRoute{"route": {BaseURL: "https://provider.example/v1", APIKeyFile: "/tmp/key", AllowedModels: []string{"model"}, MaxInflight: 1, MaxAttemptTokens: 10000, MaxAttemptCostMicros: 500000, InputCostPerMillionTokensMicros: 2500000, OutputCostPerMillionTokensMicros: 10000000}},
			CredentialRoutes: map[string]string{"cred": "route"},
		},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid isolated-only gateway config rejected: %v", err)
	}
	base.Users = []Identity{{Owner: "user", Scopes: []string{"models:invoke"}}}
	if err := base.Validate(); err == nil {
		t.Fatal("isolated-only allowed unbounded direct model invocation")
	}
	base.Users = nil
	base.ModelGateway.Routes["route"] = ModelRoute{BaseURL: "https://provider.example/v1", APIKeyFile: "/tmp/key", AllowedModels: []string{"model"}, MaxInflight: 1, MaxAttemptTokens: 10000}
	if err := base.Validate(); err == nil {
		t.Fatal("isolated-only accepted a route without an Attempt cost budget")
	}
}
