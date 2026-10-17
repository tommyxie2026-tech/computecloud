package worker

import (
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"strings"
)

func replayRuntimeMatches(b graph.ReplayBinding, profile string, r config.Runtime) bool {
	return b.Valid() && b.RuntimeProfile == profile && b.RuntimeVersion == r.Version && config.Contains(r.Models, b.Model) && b.RuntimeConfigHash == graph.Hash(job.JSON(r))
}
func replayExecutionMatches(cfg config.Worker, a *pb.Assignment) bool {
	for _, required := range a.Spec.RequiredCapabilities {
		if !strings.HasPrefix(required, "iu_replay_") {
			continue
		}
		found := false
		for _, b := range cfg.GraphReplayBindings {
			if b.Capability() == required && replayRuntimeMatches(b, a.Spec.RuntimeProfile, cfg.Runtimes[a.Spec.RuntimeProfile]) && b.Model == a.Spec.Model && b.PolicyHash == graph.Hash(job.JSON(cfg.Policies[a.Spec.PolicyRef])) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
