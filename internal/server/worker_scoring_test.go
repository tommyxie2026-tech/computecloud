package server

import (
	"fmt"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
)

func TestScoreWorkerUsesFreshSignalsAndDeterministicWeightedFactors(t *testing.T) {
	now := int64(1_000_000)
	input := workerScoreInput{
		now: now, receivedAt: now, active: 1, slots: 4,
		runtimeProfile: "codex", environmentName: "process", repositoryRef: "repo://one",
		signal: &pb.ExecutionSignal{
			ObservedAtMs: now, MeasuredAtMs: now,
			RuntimeReady: []string{"codex"}, EnvironmentReady: []string{"process"},
			RepositoryRefs: []string{"repo://one"}, WorkspacePrepareP50Ms: 30_000,
		},
	}
	score := scoreWorker(input)
	if score.RuntimeEnvironment != 100 || score.Affinity != 100 || score.PrepareCost != 50 || score.Capacity != 75 || score.Total != 86 {
		t.Fatalf("unexpected score: %+v", score)
	}
	if got := scoreWorker(input); got != score {
		t.Fatalf("score is not deterministic: %+v != %+v", got, score)
	}
}

func TestScoreWorkerUnknownAndStaleSignalsDoNotEarnReadinessCredit(t *testing.T) {
	now := int64(1_000_000)
	base := workerScoreInput{now: now, active: 0, slots: 2, runtimeProfile: "codex", environmentName: "process", repositoryRef: "repo://one"}
	unknown := scoreWorker(base)
	if unknown.RuntimeEnvironment != 0 || unknown.Affinity != 0 || unknown.PrepareCost != 0 {
		t.Fatalf("unknown signal received positive credit: %+v", unknown)
	}
	base.signal = &pb.ExecutionSignal{ObservedAtMs: now - 31_000, MeasuredAtMs: now - 31_000, RuntimeReady: []string{"codex"}, RepositoryRefs: []string{"repo://one"}, WorkspacePrepareP50Ms: 1}
	base.receivedAt = now - 31_000
	stale := scoreWorker(base)
	if stale.RuntimeEnvironment != 0 || stale.Affinity != 0 || stale.PrepareCost != 0 {
		t.Fatalf("stale signal received positive credit: %+v", stale)
	}
}

func TestScoreWorkerMonotonicReadinessAffinityCostAndCapacity(t *testing.T) {
	now := int64(1_000_000)
	base := workerScoreInput{now: now, receivedAt: now, active: 1, slots: 4, runtimeProfile: "codex", environmentName: "process", repositoryRef: "repo://one", signal: &pb.ExecutionSignal{ObservedAtMs: now, MeasuredAtMs: now}}
	low := scoreWorker(base)
	base.signal.RuntimeReady = []string{"codex"}
	base.signal.EnvironmentReady = []string{"process"}
	base.signal.RepositoryRefs = []string{"repo://one"}
	base.signal.WorkspacePrepareP50Ms = 10_000
	high := scoreWorker(base)
	if high.Total <= low.Total || high.RuntimeEnvironment < low.RuntimeEnvironment || high.Affinity < low.Affinity || high.PrepareCost < low.PrepareCost {
		t.Fatalf("improved signals did not monotonically improve score: low=%+v high=%+v", low, high)
	}
	base.active = 0
	moreCapacity := scoreWorker(base)
	if moreCapacity.Capacity <= high.Capacity || moreCapacity.Total <= high.Total {
		t.Fatalf("more free capacity did not improve score: before=%+v after=%+v", high, moreCapacity)
	}
}

func TestScoreWorkerOnlyCreditsRequiredEnvironment(t *testing.T) {
	now := int64(1_000_000)
	input := workerScoreInput{now: now, receivedAt: now, slots: 1, runtimeProfile: "codex", environmentName: "container", signal: &pb.ExecutionSignal{ObservedAtMs: now, MeasuredAtMs: now, RuntimeReady: []string{"codex"}, EnvironmentReady: []string{"process"}}}
	score := scoreWorker(input)
	if score.RuntimeEnvironment != 50 {
		t.Fatalf("unrequested environment received warmth credit: %+v", score)
	}
	input.environmentName = "process"
	if got := scoreWorker(input); got.RuntimeEnvironment != 100 {
		t.Fatalf("requested environment did not receive warmth credit: %+v", got)
	}
}

func TestRequiredEnvironmentNameDefaultsToProcess(t *testing.T) {
	if got := requiredEnvironmentName(&pb.TaskSpec{}); got != "process" {
		t.Fatalf("default environment=%q, want process", got)
	}
	if got := requiredEnvironmentName(&pb.TaskSpec{RequiredCapabilities: []string{"runtime:codex", "environment:container"}}); got != "container" {
		t.Fatalf("required environment=%q, want container", got)
	}
}

func BenchmarkChooseWorkerScoringOverhead(b *testing.B) {
	for _, count := range []int{1, 16, 64} {
		loads := make([]workerLoad, count)
		inputs := make([]workerScoreInput, count)
		for i := range loads {
			id := fmt.Sprintf("worker-%03d", i)
			loads[i] = workerLoad{peer: &session{hello: &pb.WorkerHello{WorkerId: id}}, active: i % 4, score: workerScore{RuntimeEnvironment: i % 101, Affinity: i % 101, PrepareCost: i % 101, Capacity: i % 101, Total: i % 101}}
			now := int64(1_000_000)
			inputs[i] = workerScoreInput{now: now, receivedAt: now, active: i % 4, slots: 8, runtimeProfile: "codex", environmentName: "process", repositoryRef: "repo", signal: &pb.ExecutionSignal{ObservedAtMs: now, MeasuredAtMs: now, RuntimeReady: []string{"codex"}, EnvironmentReady: []string{"process"}, RepositoryRefs: []string{"repo"}, WorkspacePrepareP50Ms: int64(i + 1)}}
		}
		b.Run(fmt.Sprintf("workers=%d/scoring=off", count), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				chooseLeastLoadedWorker(loads, "worker-031")
			}
		})
		b.Run(fmt.Sprintf("workers=%d/scoring=on", count), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := range loads {
					loads[j].score = scoreWorker(inputs[j])
				}
				chooseHighestScoredWorker(loads, "worker-031")
			}
		})
	}
}
