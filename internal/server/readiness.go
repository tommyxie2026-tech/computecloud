package server

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/readiness"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type candidateObservation struct {
	WorkerID           string       `json:"worker_id,omitempty"`
	Reason             string       `json:"reason"`
	SignalState        string       `json:"signal_state"`
	RepositoryAffinity bool         `json:"repository_affinity"`
	PrepareP50MS       *int64       `json:"prepare_p50_ms,omitempty"`
	Score              *workerScore `json:"score,omitempty"`
	SelectedReason     string       `json:"selected_reason,omitempty"`
}

func (s *Server) observeCandidate(p *session, t *pb.Task, reason string) candidateObservation {
	out := candidateObservation{WorkerID: p.hello.WorkerId, Reason: reason, SignalState: "UNKNOWN"}
	if !config.Contains(p.identity.Projects, t.Spec.ProjectId) {
		out.WorkerID = ""
		out.Reason = "SECURITY_CONSTRAINT"
		return out
	}
	s.mu.Lock()
	rawSignal := p.readiness
	signal := readiness.Fresh(rawSignal, p.readinessReceived, store.Now())
	s.mu.Unlock()
	if signal == nil {
		if rawSignal != nil {
			out.SignalState = "STALE_OR_INVALID"
		}
		return out
	}
	out.SignalState = "FRESH"
	// Affinity is explanatory only and cannot override any filter or capacity limit.
	if reason == "ELIGIBLE" {
		out.RepositoryAffinity = config.Contains(signal.RepositoryRefs, t.Spec.Workspace.RepositoryRef)
		value := signal.WorkspacePrepareP50Ms
		out.PrepareP50MS = &value
	}
	return out
}
