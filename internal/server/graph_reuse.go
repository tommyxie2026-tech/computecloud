package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"strings"
)

type graphReuseOffer struct {
	Origin GraphImportSnapshot `json:"origin"`
	Key    graph.ReuseKey      `json:"key"`
	Output json.RawMessage     `json:"output"`
}

func nodeReuseKey(f *frozenGraph, n graph.Node, m graph.ContextManifest) graph.ReuseKey {
	op := graphOperator(f, n)
	ex := graphExecutionFor(f, op)
	template := ""
	if ex != nil {
		template = f.Digests[job.TemplateKey(*ex)]
	}
	return graph.ReuseKey{NodeHash: graph.Hash(job.JSON(n)), ContextHash: m.SHA256, ReadViewHash: m.ReadViewHash, OperatorHash: graph.Hash(job.JSON(op)), SchemaHash: graph.Hash(job.JSON(f.Plan.Spec.Schemas)), TemplateHash: template, WorkspaceHash: graph.Hash(job.JSON(f.Plan.Spec.Workspace)), Binding: f.ReplayBindings[n.ReplayProfile]}
}
func validReusableOutput(raw []byte) bool {
	// UNKNOWN is an unresolved outcome, never a reusable success. Inspect nested
	// values so a structured verdict cannot be hidden inside a result object.
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var unresolved func(any) bool
	unresolved = func(v any) bool {
		switch x := v.(type) {
		case string:
			return strings.EqualFold(x, "UNKNOWN")
		case []any:
			for _, v := range x {
				if unresolved(v) {
					return true
				}
			}
		case map[string]any:
			for _, v := range x {
				if unresolved(v) {
					return true
				}
			}
		}
		return false
	}
	return !unresolved(value)
}
func freezeReuseOfferTx(ctx context.Context, q store.Query, source, target *Job, in GraphImportRequest, nodeID string, view graph.MemoryView, artifacts []string) error {
	n, ok := graphNode(source.graph, in.NodeID)
	if !ok {
		return importFailure("SOURCE_NODE")
	}
	op := graphOperator(source.graph, n)
	if n.ReplayProfile == "" || op.Kind != "agent" || op.Execution == nil || !op.Execution.ReplaySafe || !source.graph.ReplayBindings[n.ReplayProfile].Valid() {
		return importFailure("RESULT_REPLAY_UNPROVEN")
	}
	var raw, taskSpec, input []byte
	var state string
	if e := q.QueryRowContext(ctx, `SELECT n.context_json,n.output_json,n.state,t.spec FROM iu_nodes n JOIN tasks t ON t.id=n.task_id WHERE n.goal_id=? AND n.generation=? AND n.node_id=?`, source.graph.GoalID, source.graph.Generation, in.NodeID).Scan(&raw, &input, &state, &taskSpec); e != nil {
		return e
	}
	var manifest graph.ContextManifest
	if e := json.Unmarshal(raw, &manifest); e != nil {
		return e
	}
	var task pb.TaskSpec
	if e := decode(taskSpec, &task); e != nil {
		return e
	}
	binding := source.graph.ReplayBindings[n.ReplayProfile]
	hasCapability := false
	for _, capability := range task.RequiredCapabilities {
		if capability == binding.Capability() {
			hasCapability = true
		}
	}
	if !hasCapability || state != "SUCCEEDED" {
		return importFailure("RESULT_REPLAY_UNPROVEN")
	}
	if len(view.Records) != len(in.RecordIDs) {
		return importFailure("RESULT_REQUIRES_FULL_VIEW")
	}
	identity, e := importIdentityTx(ctx, q, source, in.NodeID)
	if e != nil {
		return e
	}
	memory, e := graph.ImportView(view, in.RecordIDs, fmt.Sprintf("%s/%d/%s/%s", target.graph.GoalID, target.graph.Generation, nodeID, in.ID), identity)
	if e != nil {
		return e
	}
	offer := graphReuseOffer{Key: nodeReuseKey(source.graph, n, manifest), Output: input, Origin: GraphImportSnapshot{GrantID: in.ID, SourceJob: source.ID, SourceGoal: source.graph.GoalID, SourceGeneration: source.graph.Generation, SourceNode: in.NodeID, SourceRevision: in.ExpectedRevision, Memory: memory, TargetGeneration: target.graph.Generation, TargetNode: nodeID}}
	if !offer.Key.Valid() {
		return importFailure("RESULT_KEY_UNPROVEN")
	}
	if _, e = q.ExecContext(ctx, `INSERT INTO iu_reuse_offers VALUES(?,?,?,?,?,?)`, target.graph.GoalID, target.graph.Generation, nodeID, in.ID, job.JSON(offer), job.JSON(artifacts)); e != nil {
		return e
	}
	for _, a := range artifacts {
		if e = pinArtifactRef(ctx, q, a, "iu_memory", fmt.Sprintf("reuse/%s/%d/%s", target.graph.GoalID, target.graph.Generation, nodeID)); e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) activateReuseTx(ctx context.Context, q store.Query, j *Job, n graph.Node, manifest graph.ContextManifest, input graph.MemoryView, row *graphNodeRow) (bool, error) {
	f := j.graph
	var raw []byte
	e := q.QueryRowContext(ctx, `SELECT body FROM iu_reuse_offers WHERE goal_id=? AND generation=? AND node_id=?`, f.GoalID, f.Generation, n.ID).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		for _, b := range f.Plan.Spec.Reuse {
			if b.NodeID == n.ID {
				return false, importFailure("REUSE_OFFER_MISSING")
			}
		}
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var existing int
	if e = q.QueryRowContext(ctx, `SELECT count(*) FROM iu_node_reuse WHERE goal_id=? AND generation=? AND node_id=?`, f.GoalID, f.Generation, n.ID).Scan(&existing); e != nil {
		return false, e
	}
	if existing > 0 {
		return false, nil
	}
	var offer graphReuseOffer
	if e = json.Unmarshal(raw, &offer); e != nil {
		return false, e
	}
	source, e := readJob(ctx, q, offer.Origin.SourceJob)
	if e != nil {
		return false, e
	}
	revision, e := graphValidityRevisionTx(ctx, q, source.graph)
	if e != nil {
		return false, e
	}
	valid := revision == offer.Origin.SourceRevision && validReusableOutput(offer.Output)
	if len(offer.Origin.Memory.View.Conflicts) > 0 {
		valid = false
	}
	for _, r := range offer.Origin.Memory.View.Records {
		if r.Validity != "VALID" || r.Kind == "unresolved" {
			valid = false
		}
	}
	decision := graph.DecideReuse(offer.Key, nodeReuseKey(f, n, manifest), valid)
	decision.SourceJob = source.ID
	decision.SourceNode = offer.Origin.SourceNode
	decision.SourceGeneration = source.graph.Generation
	if _, e = q.ExecContext(ctx, `INSERT INTO iu_node_reuse VALUES(?,?,?,?,?)`, f.GoalID, f.Generation, n.ID, job.JSON(decision), store.Now()); e != nil {
		return false, e
	}
	if decision.Decision != "REUSE" {
		return false, nil
	}
	// No Task, Attempt or Admission is invented. The source Artifact remains pinned.
	row.State = "SUCCEEDED"
	row.Output = offer.Output
	row.View = job.JSON(offer.Origin.Memory.View)
	row.Context = job.JSON(manifest)
	row.InputView = job.JSON(input)
	if _, e = q.ExecContext(ctx, `UPDATE iu_nodes SET state='SUCCEEDED',context_json=?,input_view=?,output_json=?,output_view=? WHERE goal_id=? AND generation=? AND node_id=? AND state='BLOCKED'`, row.Context, row.InputView, row.Output, row.View, f.GoalID, f.Generation, n.ID); e != nil {
		return false, e
	}
	e = appendJobEvent(ctx, q, j.ID, "graph.node_reused", map[string]any{"node_id": n.ID, "source_job": source.ID, "source_node": offer.Origin.SourceNode, "source_generation": source.graph.Generation, "key_sha256": decision.KeyHash}, "", "")
	return true, e
}
