package worker

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/workspace"
	"testing"
)

func TestReadinessWorkerSamplesAndRestart(t *testing.T) {
	w := &Worker{cfg: config.Worker{Slots: 2}, hello: &pb.WorkerHello{Runtimes: []*pb.Runtime{{Profile: "codex_exec"}}}}
	if w.executionSignal() != nil {
		t.Fatal("fabricated startup latency")
	}
	w.recordPreparation(workspace.WorkspaceTemplate{TemplateID: "a", RepositoryRef: "repo", BaseCommit: "base", Version: 1}, workspace.PrepareMetrics{PrepareMS: 10, MaterializeMS: 20})
	w.recordEnvironmentSignal("process", 5)
	s := w.executionSignal()
	if s == nil || s.WorkspacePrepareP50Ms != 30 || s.EnvironmentStartupP50Ms != 5 || len(s.EnvironmentReady) != 1 {
		t.Fatalf("sample=%+v", s)
	}
	for i := 0; i < 64; i++ {
		w.recordPreparation(workspace.WorkspaceTemplate{RepositoryRef: "repo"}, workspace.PrepareMetrics{})
	}
	if len(w.preparations) != 32 {
		t.Fatal("unbounded samples")
	}
	for i := range w.preparations {
		w.preparations[i].at = store.Now() - 31000
	}
	if w.executionSignal() != nil {
		t.Fatal("stale sample refreshed by heartbeat")
	}
	restarted := &Worker{cfg: w.cfg, hello: w.hello}
	if restarted.executionSignal() != nil {
		t.Fatal("restart retained stale signal")
	}
}
