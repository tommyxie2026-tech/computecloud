package worker

import (
	"fmt"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
)

func prepareGraph(a *pb.Assignment) (*jobRun, error) {
	var inv graph.Invocation
	if a.Job == nil || len(a.Job.GraphExecutionJson) > 256<<10 {
		return nil, fmt.Errorf("IU_INVOCATION_LIMIT")
	}
	if e := jsonutil.Decode(a.Job.GraphExecutionJson, &inv); e != nil {
		return nil, e
	}
	if inv.Version != "computecloud-job-io-v2" || inv.GoalID == "" || inv.Generation < 1 || inv.NodeID == "" || len(inv.Context.Text) > 64<<10 || inv.Context.SHA256 != graph.Hash([]byte(a.Spec.Input.Text)) || a.Spec.Input.Text != inv.Context.Text || inv.Context.ReadViewHash != inv.View.SHA256 {
		return nil, fmt.Errorf("IU_CONTEXT_MISMATCH")
	}
	if e := graph.ValidateView(inv.View); e != nil {
		return nil, e
	}
	if graph.HasMemoryOrigins(inv.View) {
		negotiated := false
		for _, c := range a.Spec.RequiredCapabilities {
			if c == graph.MemoryOriginCapability {
				negotiated = true
			}
		}
		if !negotiated {
			return nil, fmt.Errorf("IU_MEMORY_ORIGIN_CAPABILITY_REQUIRED")
		}
	}
	if e := inv.Context.ValidateInput(a.Spec.Model, a.Spec.Input.Text); e != nil {
		return nil, e
	}
	return &jobRun{prompt: inv.Context.Text, graph: &inv}, nil
}

func finishGraphOutput(result string, diff []byte, run *jobRun) (map[string][]byte, error) {
	// v2 transfers structured values and memory, not mutable workspace state.
	if len(diff) != 0 {
		return nil, fmt.Errorf("IU_WORKSPACE_MUTATION_UNSUPPORTED")
	}
	out, e := graph.BuildOutput([]byte(result), *run.graph)
	if e != nil {
		return nil, e
	}
	return map[string][]byte{"iu-output.json": job.JSON(out)}, nil
}
