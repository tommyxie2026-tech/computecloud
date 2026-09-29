package config

import (
	"strings"
	"testing"
)

func TestJobTemplateAllowsProviderNeutralRuntimeProfile(t *testing.T) {
	s := Server{}
	s.Jobs.Enabled = true
	s.DefaultV02()
	s.Jobs.Templates = []JobTemplate{{
		RuntimeProfile: "gemini_cli",
		PolicyRef: "policy",
		AcceptanceProfile: "verify",
		Digest: strings.Repeat("a", 64),
	}}
	if err := s.ValidateV02(); err != nil {
		t.Fatalf("provider-neutral template rejected: %v", err)
	}
}

func TestWorkerRejectsUnsafeGeminiApprovalMode(t *testing.T) {
	w := Worker{
		ID: "w", Address: "127.0.0.1:1", DataDir: t.TempDir(), Slots: 1,
		Runtimes: map[string]Runtime{"gemini_cli": {Executable: "gemini", Version: "v1"}},
		Policies: map[string]Policy{"p": {GeminiApprovalMode: "yolo"}},
	}
	if err := w.Validate(); err == nil {
		t.Fatal("unsafe gemini approval mode accepted")
	}
}
