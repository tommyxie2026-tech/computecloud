package worker

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/readiness"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/workspace"
	"sort"
)

type environmentSample struct {
	name        string
	elapsed, at int64
}

func (w *Worker) recordEnvironmentSignal(name string, elapsed int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.environments = append(w.environments, environmentSample{name, elapsed, store.Now()})
	if len(w.environments) > 32 {
		w.environments = append([]environmentSample(nil), w.environments[len(w.environments)-32:]...)
	}
}

type preparationSample struct {
	fingerprint, repository string
	elapsed, at             int64
}

func (w *Worker) recordPreparation(t workspace.WorkspaceTemplate, m workspace.PrepareMetrics) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.preparations = append(w.preparations, preparationSample{t.Fingerprint(), t.RepositoryRef, m.PrepareMS + m.MaterializeMS, store.Now()})
	if len(w.preparations) > 32 {
		w.preparations = append([]preparationSample(nil), w.preparations[len(w.preparations)-32:]...)
	}
}
func (w *Worker) executionSignal() *pb.ExecutionSignal {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := store.Now()
	out := &pb.ExecutionSignal{ObservedAtMs: now, FreeSlots: int32(w.cfg.Slots - len(w.runs))}
	if out.FreeSlots < 0 {
		out.FreeSlots = 0
	}
	var costs []int64
	fingerprints, repositories := map[string]bool{}, map[string]bool{}
	for _, sample := range w.preparations {
		if sample.at > now || now-sample.at > readiness.TTLMS {
			continue
		}
		costs = append(costs, sample.elapsed)
		fingerprints[sample.fingerprint] = true
		repositories[sample.repository] = true
		if sample.at > out.MeasuredAtMs {
			out.MeasuredAtMs = sample.at
		}
	}
	if len(costs) == 0 {
		return nil
	}
	sort.Slice(costs, func(i, j int) bool { return costs[i] < costs[j] })
	out.WorkspacePrepareP50Ms = costs[len(costs)/2]
	out.TemplateFingerprints = keys(fingerprints)
	out.RepositoryRefs = keys(repositories)
	if w.hello != nil {
		for _, r := range w.hello.Runtimes {
			out.RuntimeReady = append(out.RuntimeReady, r.Profile)
		}
	}
	environmentNames := map[string]bool{}
	var startup []int64
	for _, sample := range w.environments {
		if sample.at <= now && now-sample.at <= readiness.TTLMS {
			environmentNames[sample.name] = true
			startup = append(startup, sample.elapsed)
		}
	}
	out.EnvironmentReady = keys(environmentNames)
	if len(startup) > 0 {
		sort.Slice(startup, func(i, j int) bool { return startup[i] < startup[j] })
		out.EnvironmentStartupP50Ms = startup[len(startup)/2]
	}
	return out
}
