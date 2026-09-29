package job

import (
	"encoding/json"
	"testing"
)

func TestExecutionAllowsProviderNeutralRuntimeProfile(t *testing.T) {
	e := Execution{
		RuntimeProfile: "gemini_cli",
		Model: "gemini-fixture",
		CredentialRef: "cred",
		PolicyRef: "policy",
		AcceptanceProfile: "verify",
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("provider-neutral runtime rejected: %v", err)
	}
}

func TestExecutionKeepsLegacyEngineAliasFenced(t *testing.T) {
	ok := Execution{
		Engine: "codex", RuntimeProfile: "codex_exec", Model: "m",
		CredentialRef: "cred", PolicyRef: "policy", AcceptanceProfile: "verify",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("legacy codex alias rejected: %v", err)
	}
	bad := ok
	bad.RuntimeProfile = "gemini_cli"
	if err := bad.Validate(); err == nil {
		t.Fatal("legacy engine alias was allowed to select another runtime")
	}
}

func TestJobSchemaAllowsProviderNeutralRuntimeWithoutLegacyEngine(t *testing.T) {
	raw := map[string]any{
		"schema_version": "v0.2",
		"project_id": "project",
		"mode": "single",
		"workspace": map[string]any{
			"repository_ref": "repo",
			"base_commit": "0123456789012345678901234567890123456789",
		},
		"input": map[string]any{"text": "do work"},
		"execution": map[string]any{
			"runtime_profile": "gemini_cli",
			"model": "gemini-fixture",
			"credential_ref": "cred",
			"policy_ref": "policy",
			"acceptance_profile": "verify",
		},
		"limits": map[string]any{
			"timeout_seconds": 60,
			"max_attempts_per_task": 1,
		},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(b, 32, 8); err != nil {
		t.Fatalf("provider-neutral Job schema rejected Gemini: %v", err)
	}
}
