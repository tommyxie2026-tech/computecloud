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
		Runtimes: map[string]Runtime{"codex_exec": {Version: "v"}},
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
