package server

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"testing"
)

func TestReadinessCannotOverrideHardFilter(t *testing.T) {
	now := store.Now()
	s := &Server{}
	p := &session{hello: &pb.WorkerHello{WorkerId: "worker"}, identity: config.Identity{Projects: []string{"project"}}, readiness: &pb.ExecutionSignal{ObservedAtMs: now, MeasuredAtMs: now, RepositoryRefs: []string{"repo"}, WorkspacePrepareP50Ms: 1}, readinessReceived: now}
	task := &pb.Task{Spec: &pb.TaskSpec{ProjectId: "project", Workspace: &pb.Workspace{RepositoryRef: "repo"}}}
	if fits(p, task) {
		t.Fatal("affinity bypassed missing capability")
	}
	rejected := s.observeCandidate(p, task, "TEMPLATE_OR_CAPABILITY_MISMATCH")
	if rejected.RepositoryAffinity || rejected.PrepareP50MS != nil {
		t.Fatal("rejected candidate received affinity credit")
	}
	eligible := s.observeCandidate(p, task, "ELIGIBLE")
	if !eligible.RepositoryAffinity || eligible.PrepareP50MS == nil {
		t.Fatal("fresh hint missing")
	}
	p.readinessReceived = now - 31000
	if s.observeCandidate(p, task, "ELIGIBLE").SignalState != "UNKNOWN" {
		t.Fatal("stale signal accepted")
	}
	p.identity.Projects = nil
	if s.observeCandidate(p, task, "ELIGIBLE").WorkerID != "" {
		t.Fatal("unauthorized worker identity exposed")
	}
}
