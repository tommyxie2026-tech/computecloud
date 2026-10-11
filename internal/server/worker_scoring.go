package server

import (
	"sort"
	"strings"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/readiness"
)

const workerScoreVersion = "readiness-v1"

type workerScoreInput struct {
	now, receivedAt int64
	active, slots   int
	runtimeProfile  string
	environmentName string
	repositoryRef   string
	signal          *pb.ExecutionSignal
}

type workerScore struct {
	RuntimeEnvironment int `json:"runtime_environment"`
	Affinity           int `json:"affinity"`
	PrepareCost        int `json:"prepare_cost"`
	Capacity           int `json:"capacity"`
	Total              int `json:"total"`
}

func scoreWorker(input workerScoreInput) workerScore {
	out := workerScore{}
	signal := readiness.Fresh(input.signal, input.receivedAt, input.now)
	if signal != nil {
		runtimeReady := false
		for _, profile := range signal.RuntimeReady {
			if profile == input.runtimeProfile {
				runtimeReady = true
				break
			}
		}
		if runtimeReady {
			out.RuntimeEnvironment += 50
		}
		for _, environment := range signal.EnvironmentReady {
			if environment == input.environmentName {
				out.RuntimeEnvironment += 50
				break
			}
		}
		for _, repository := range signal.RepositoryRefs {
			if repository == input.repositoryRef {
				out.Affinity = 100
				break
			}
		}
		const maxPrepareMS int64 = 60_000
		if signal.WorkspacePrepareP50Ms > 0 && signal.WorkspacePrepareP50Ms <= maxPrepareMS {
			out.PrepareCost = int((maxPrepareMS - signal.WorkspacePrepareP50Ms) * 100 / maxPrepareMS)
		}
	}
	if input.slots > 0 && input.active < input.slots {
		out.Capacity = (input.slots - input.active) * 100 / input.slots
	}
	out.Total = (35*out.RuntimeEnvironment + 30*out.Affinity + 20*out.PrepareCost + 15*out.Capacity + 50) / 100
	return out
}

func requiredEnvironmentName(spec *pb.TaskSpec) string {
	for _, capability := range spec.GetRequiredCapabilities() {
		if strings.HasPrefix(capability, "environment:") {
			return strings.TrimPrefix(capability, "environment:")
		}
	}
	return "process"
}

func chooseHighestScoredWorker(loads []workerLoad, cursor string) (*session, string) {
	if len(loads) == 0 {
		return nil, cursor
	}
	ordered := append([]workerLoad(nil), loads...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].score.Total != ordered[j].score.Total {
			return ordered[i].score.Total > ordered[j].score.Total
		}
		if ordered[i].active != ordered[j].active {
			return ordered[i].active < ordered[j].active
		}
		return ordered[i].peer.hello.WorkerId < ordered[j].peer.hello.WorkerId
	})
	top := ordered[0]
	tied := make([]workerLoad, 0, len(ordered))
	for _, candidate := range ordered {
		if candidate.score.Total != top.score.Total || candidate.active != top.active {
			break
		}
		tied = append(tied, candidate)
	}
	return chooseLeastLoadedWorker(tied, cursor)
}
