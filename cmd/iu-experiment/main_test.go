package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseCLIRequiresFinalSuccessAndRejectsToolUse(t *testing.T) {
	raw := []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"version\\\":\\\"iu-node-output-v1\\\",\\\"values\\\":{\\\"result\\\":11000},\\\"records\\\":[]}\"}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":8}}\n")
	r, e := parseCLI("codex_exec", raw)
	if e != nil || r.InputTokens == nil || *r.InputTokens != 7 {
		t.Fatalf("%+v %v", r, e)
	}
	if _, e = parseCLI("codex_exec", append([]byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\"}}\n"), raw...)); e == nil {
		t.Fatal("tool use accepted")
	}
	if _, e = parseCLI("codex_exec", []byte(`{"type":"turn.failed","error":{"message":"failed"}}`)); e == nil {
		t.Fatal("failed invocation accepted")
	}
}

func TestCLIErrorCodesDoNotCopySecrets(t *testing.T) {
	if classifyFailure([]byte("failed to initialize in-process app-server client: Operation not permitted")) != "CLI_IPC_PERMISSION_DENIED" {
		t.Fatal("lost startup diagnosis")
	}
	if classifyFailure([]byte("Request too large (max 32MB)")) != "CLI_REQUEST_TOO_LARGE" {
		t.Fatal("lost proxy diagnosis")
	}
	if classifyFailure([]byte("secret=not-a-real-secret")) != "CLI_OR_PROTOCOL_FAILED" {
		t.Fatal("unsafe error copied")
	}
}

func TestCLIProcessTimeoutDrainsWholeGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, result := runCLI(ctx, "/bin/sh", []string{"-c", "sleep 20 & wait"}, t.TempDir(), strings.NewReader(""))
	if !result.Cleanup && result.Err != nil && strings.Contains(strings.ToLower(result.Err.Error()), "operation not permitted") {
		if os.Getenv("CI") == "true" {
			t.Fatal("CI must exercise process cleanup; identity inspection unavailable")
		}
		t.Skip("host sandbox cannot inspect process identity; execution fails closed")
	}
	if !errors.Is(result.Err, context.DeadlineExceeded) || !result.Cleanup || !result.TermSent {
		t.Fatalf("CLI group did not drain on timeout: %+v", result)
	}
}
