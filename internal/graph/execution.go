package graph

import (
	"encoding/json"
	"fmt"

	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
)

type Invocation struct {
	Schemas        map[string]json.RawMessage `json:"schemas"`
	Version        string                     `json:"version"`
	GoalID         string                     `json:"goal_id"`
	Generation     int64                      `json:"graph_generation"`
	NodeID         string                     `json:"node_id"`
	Context        ContextManifest            `json:"context"`
	View           MemoryView                 `json:"view"`
	Outputs        []Port                     `json:"outputs"`
	Kind           string                     `json:"kind"`
	CommandProfile string                     `json:"command_profile,omitempty"`
}
type NodeOutput struct {
	Version string                     `json:"version"`
	Values  map[string]json.RawMessage `json:"values"`
	Records []MemoryRecord             `json:"records"`
}
type ExecutionOutput struct {
	Version     string                     `json:"version"`
	GoalID      string                     `json:"goal_id"`
	Generation  int64                      `json:"graph_generation"`
	NodeID      string                     `json:"node_id"`
	ContextHash string                     `json:"context_sha256"`
	Values      map[string]json.RawMessage `json:"values"`
	Delta       Delta                      `json:"delta"`
}

func BuildOutput(raw []byte, inv Invocation) (ExecutionOutput, error) {
	var out ExecutionOutput
	if len(raw) > 64<<10 {
		return out, fmt.Errorf("IU_OUTPUT_LIMIT")
	}
	var model NodeOutput
	if e := jsonutil.Decode(raw, &model); e != nil {
		return out, e
	}
	if model.Version != "iu-node-output-v1" || len(model.Values) != len(inv.Outputs) {
		return out, invalid("node output ports")
	}
	for _, port := range inv.Outputs {
		v, ok := model.Values[port.Name]
		if !ok || !json.Valid(v) {
			return out, invalid("missing node output")
		}
	}
	if e := ValidateValues(model.Values, inv.Outputs, inv.Schemas); e != nil {
		return out, e
	}
	d := Delta{Version: "iu-memory-delta-v1", ReadViewHash: inv.View.SHA256, ContextHash: inv.Context.SHA256, Records: model.Records}
	d, e := BindDeltaSources(d, inv.Context.Spans)
	if e != nil {
		return out, e
	}
	checked, e := ValidateDelta(canonical(d), inv.View)
	if e != nil {
		return out, e
	}
	out = ExecutionOutput{Version: "iu-execution-output-v2", GoalID: inv.GoalID, Generation: inv.Generation, NodeID: inv.NodeID, ContextHash: inv.Context.SHA256, Values: model.Values, Delta: checked}
	if len(canonical(out)) > 64<<10 {
		return ExecutionOutput{}, fmt.Errorf("IU_OUTPUT_LIMIT")
	}
	return out, nil
}
