package config

import "testing"

func TestCLIRouteValidation(t *testing.T) {
	good := ModelRoute{Backend: "codex_cli", CLI: &CodexCLI{Executable: "/usr/bin/codex", Version: "codex-cli 0.154.0"}, AllowedModels: []string{"model"}, MaxInflight: 1}
	for _, tc := range []struct {
		name   string
		change func(*ModelRoute)
		valid  bool
	}{
		{"valid", func(*ModelRoute) {}, true},
		{"missing CLI", func(r *ModelRoute) { r.CLI = nil }, false},
		{"mixed credentials", func(r *ModelRoute) { r.APIKeyFile = "key" }, false},
		{"mixed upstream", func(r *ModelRoute) { r.BaseURL = "https://example.com/v1" }, false},
		{"unknown backend", func(r *ModelRoute) { r.Backend = "unknown" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			tc.change(&r)
			c := Server{ModelGateway: ModelGateway{Enabled: true, Routes: map[string]ModelRoute{"cli": r}}, Users: []Identity{{Owner: "a", Projects: []string{"p"}, Scopes: []string{"models:invoke"}, ModelProject: "p", ModelRoute: "cli"}}}
			c.DefaultV02()
			if err := c.ValidateV02(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	changed := good
	changed.CLI = &CodexCLI{Executable: "/usr/bin/codex", Version: "another-version"}
	if RouteDigest(good) == RouteDigest(changed) {
		t.Fatal("CLI version missing from route digest")
	}
}
