package config

import "testing"

func TestAllowedToolsAffectTemplateDigest(t *testing.T) {
	base := Policy{CodexSandbox: "read-only"}
	withTool := Policy{CodexSandbox: "read-only", AllowedTools: []string{"job_io_v1"}}
	a := TemplateDigest("runtime-v1", base, nil)
	b := TemplateDigest("runtime-v1", withTool, nil)
	if a == b {
		t.Fatal("tool policy did not alter template digest")
	}
}

func TestWorkerPolicyToolNameValidation(t *testing.T) {
	base := Worker{
		ID: "w", Address: "127.0.0.1:1", DataDir: t.TempDir(), Slots: 1,
		Runtimes: map[string]Runtime{"codex_exec": {Version: "v"}},
		Repositories: map[string]string{"repo": t.TempDir()},
	}
	for _, tools := range [][]string{{"Bad"}, {"tool:qualified"}, {"job_io_v1", "job_io_v1"}} {
		w := base
		w.Policies = map[string]Policy{"p": {AllowedTools: tools}}
		if err := w.Validate(); err == nil {
			t.Fatalf("invalid policy tools accepted: %v", tools)
		}
	}
}
