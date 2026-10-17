package worker

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"testing"
)

func TestGraphReplayBindingRejectsWorkerDrift(t *testing.T) {
	runtime := config.Runtime{Executable: "/pinned/fixture", Version: "1", Models: []string{"m"}, Credentials: []string{"account"}}
	policy := config.Policy{CodexSandbox: "read-only"}
	b := graph.ReplayBinding{Version: "1", RuntimeProfile: "codex_exec", RuntimeVersion: "1", Model: "m", EnvironmentHash: graph.Hash([]byte("audited image")), RuntimeConfigHash: graph.Hash(job.JSON(runtime)), PolicyHash: graph.Hash(job.JSON(policy)), ReadOnly: true}
	cfg := config.Worker{Runtimes: map[string]config.Runtime{"codex_exec": runtime}, Policies: map[string]config.Policy{"review": policy}, GraphReplayBindings: map[string]graph.ReplayBinding{"pinned": b}}
	a := &pb.Assignment{Spec: &pb.TaskSpec{RuntimeProfile: "codex_exec", Model: "m", PolicyRef: "review", RequiredCapabilities: []string{b.Capability()}}}
	if !replayExecutionMatches(cfg, a) {
		t.Fatal("matching binding denied")
	}
	changed := runtime
	changed.Version = "2"
	cfg.Runtimes["codex_exec"] = changed
	if replayExecutionMatches(cfg, a) || replayRuntimeMatches(b, "codex_exec", changed) {
		t.Fatal("runtime drift accepted")
	}
	cfg.Runtimes["codex_exec"] = runtime
	cfg.Policies["review"] = config.Policy{CodexSandbox: "workspace-write"}
	if replayExecutionMatches(cfg, a) {
		t.Fatal("policy drift accepted")
	}
	cfg.Policies["review"] = policy
	a.Spec.Model = "other"
	if replayExecutionMatches(cfg, a) {
		t.Fatal("model switch accepted")
	}
	a.Spec.Model = "m"
	cfg.GraphReplayBindings = nil
	if replayExecutionMatches(cfg, a) {
		t.Fatal("missing binding accepted")
	}
}
