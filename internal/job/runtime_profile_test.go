package job

import "testing"

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
