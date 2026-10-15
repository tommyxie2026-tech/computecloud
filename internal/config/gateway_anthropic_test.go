package config

import (
	"os"
	"path/filepath"
	"testing"
)

func validAnthropicRoute(keyFile string) ModelRoute {
	return ModelRoute{
		Backend: "anthropic_messages", BaseURL: "https://gateway.example.invalid/v1",
		APIKeyFile: keyFile, APIKeyRef: "anthropic-prod-key-v1", ProtocolVersion: "2023-06-01",
		AllowedModels: []string{"claude-safe"}, MaxInflight: 2, MaxAttemptTokens: 20000,
		MaxAttemptCostMicros: 500000, InputCostPerMillionTokensMicros: 3000000,
		OutputCostPerMillionTokensMicros: 15000000,
	}
}

func anthropicConfig(route ModelRoute) Server {
	c := Server{ModelGateway: ModelGateway{
		Enabled: true, MaxRequestBytes: 1 << 20, MaxInflight: 4,
		ConnectTimeoutSeconds: 2, HeaderTimeoutSeconds: 2,
		StreamIdleTimeoutSeconds: 5, RequestTimeoutSeconds: 10,
		Routes: map[string]ModelRoute{"anthropic": route},
	}}
	c.DefaultV02()
	return c
}

func TestAnthropicGatewayRouteValidation(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "upstream.key")
	if err := os.WriteFile(keyFile, []byte("test-key-value"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*ModelRoute)
		valid  bool
	}{
		{"valid", func(*ModelRoute) {}, true},
		{"HTTP downgrade", func(r *ModelRoute) { r.BaseURL = "http://gateway.example.invalid/v1" }, false},
		{"URL query", func(r *ModelRoute) { r.BaseURL += "?tenant=1" }, false},
		{"URL userinfo", func(r *ModelRoute) { r.BaseURL = "https://user:pass@gateway.example.invalid/v1" }, false},
		{"missing key file", func(r *ModelRoute) { r.APIKeyFile = filepath.Join(dir, "missing.key") }, false},
		{"missing key reference", func(r *ModelRoute) { r.APIKeyRef = "" }, false},
		{"missing model allowlist", func(r *ModelRoute) { r.AllowedModels = nil }, false},
		{"missing budget", func(r *ModelRoute) { r.MaxAttemptTokens = 0 }, false},
		{"partial cost budget", func(r *ModelRoute) { r.OutputCostPerMillionTokensMicros = 0 }, false},
		{"wrong backend with CLI", func(r *ModelRoute) { r.CLI = &CodexCLI{Executable: "codex", Version: "1"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := validAnthropicRoute(keyFile)
			tc.change(&route)
			if err := anthropicConfig(route).ValidateV02(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestAnthropicRouteDigestCoversProtocolModelAndKeyIdentity(t *testing.T) {
	route := validAnthropicRoute("/secret/path")
	base := RouteDigest(route)
	for name, mutate := range map[string]func(*ModelRoute){
		"protocol":     func(r *ModelRoute) { r.ProtocolVersion = "2024-01-01" },
		"model":        func(r *ModelRoute) { r.AllowedModels = []string{"claude-other"} },
		"key identity": func(r *ModelRoute) { r.APIKeyRef = "anthropic-prod-key-v2" },
		"budget":       func(r *ModelRoute) { r.MaxAttemptTokens++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := route
			mutate(&changed)
			if RouteDigest(changed) == base {
				t.Fatalf("route digest did not change for %s", name)
			}
		})
	}
	changedPath := route
	changedPath.APIKeyFile = "/a/different/mount/path"
	if RouteDigest(changedPath) != base {
		t.Fatal("secret file mount path must not be included in the public route identity")
	}
}
