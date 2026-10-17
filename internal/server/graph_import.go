package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GraphImportRequest struct {
	Purpose          string   `json:"purpose,omitempty"`
	ID               string   `json:"id"`
	NodeID           string   `json:"node_id"`
	ViewHash         string   `json:"view_sha256"`
	RecordIDs        []string `json:"record_ids"`
	ExpectedRevision int64    `json:"expected_revision"`
	TargetGeneration int64    `json:"target_generation"`
	TargetPlanHash   string   `json:"target_plan_sha256"`
	ExpiresAt        int64    `json:"expires_at"`
}
type GraphImportGrant struct {
	Request          GraphImportRequest    `json:"request"`
	GoalID           string                `json:"goal_id"`
	SourceJob        string                `json:"source_job"`
	SourceGeneration int64                 `json:"source_generation"`
	Actor            string                `json:"actor"`
	State            string                `json:"state"`
	TargetJob        string                `json:"target_job,omitempty"`
	Snapshots        []GraphImportSnapshot `json:"snapshots,omitempty"`
}
type GraphImportSnapshot struct {
	GrantID          string               `json:"grant_id"`
	SourceJob        string               `json:"source_job"`
	SourceGoal       string               `json:"source_goal"`
	SourceGeneration int64                `json:"source_generation"`
	SourceNode       string               `json:"source_node"`
	SourceRevision   int64                `json:"source_validity_revision"`
	Memory           graph.ImportedMemory `json:"memory"`
	TargetGeneration int64                `json:"target_generation"`
	TargetNode       string               `json:"target_node"`
}

func importFailure(reason string) error {
	return status.Error(codes.FailedPrecondition, "IMPORT_"+reason)
}
func importGrantRef(goalID, id string) string { return "import-grant/" + goalID + "/" + id }
func readImportGrantTx(ctx context.Context, q store.Query, j *Job, id string) (*GraphImportGrant, string, []string, error) {
	out := &GraphImportGrant{GoalID: j.graph.GoalID, SourceJob: j.ID, SourceGeneration: j.graph.Generation}
	var raw, artifacts []byte
	var digest string
	e := q.QueryRowContext(ctx, `SELECT g.body,g.actor,g.issuer_digest,g.artifacts,s.state,s.target_job FROM iu_import_grants g JOIN iu_import_grant_state s USING(goal_id,grant_id) WHERE g.goal_id=? AND g.grant_id=? AND g.source_job=?`, j.graph.GoalID, id, j.ID).Scan(&raw, &out.Actor, &digest, &artifacts, &out.State, &out.TargetJob)
	if e != nil {
		return nil, "", nil, e
	}
	if e = json.Unmarshal(raw, &out.Request); e != nil {
		return nil, "", nil, e
	}
	var ids []string
	e = json.Unmarshal(artifacts, &ids)
	return out, digest, ids, e
}
func checkImportSourceTx(ctx context.Context, q store.Query, j *Job, in GraphImportRequest) (graph.MemoryView, error) {
	var empty graph.MemoryView
	revision, e := graphValidityRevisionTx(ctx, q, j.graph)
	if e != nil {
		return empty, e
	}
	if revision != in.ExpectedRevision {
		return empty, importFailure("STALE_VALIDITY")
	}
	v, e := graphMemoryTx(ctx, q, j.graph, in.NodeID)
	if e != nil {
		return empty, e
	}
	if v.SHA256 != in.ViewHash {
		return empty, importFailure("VIEW_MISMATCH")
	}
	if _, e = graph.ImportView(v, in.RecordIDs, j.graph.GoalID+"/"+in.ID); e != nil {
		return empty, importFailure(e.Error())
	}
	return v, nil
}
func collectImportArtifactsTx(ctx context.Context, q store.Query, f *frozenGraph) ([]string, error) {
	set := map[string]bool{}
	rows, e := q.QueryContext(ctx, `SELECT artifact_id FROM iu_admissions WHERE goal_id=? AND generation=? AND state='ADMITTED'`, f.GoalID, f.Generation)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		set[id] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = q.QueryContext(ctx, `SELECT artifacts FROM iu_import_snapshots WHERE goal_id=? AND generation=? UNION ALL SELECT artifacts FROM iu_reuse_offers WHERE goal_id=? AND generation=?`, f.GoalID, f.Generation, f.GoalID, f.Generation)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var raw []byte
		var ids []string
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal(raw, &ids); e != nil {
			rows.Close()
			return nil, e
		}
		for _, id := range ids {
			set[id] = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(set) == 0 || len(set) > 128 {
		return nil, importFailure("ARTIFACT_BOUNDS")
	}
	ids := []string{}
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
func (s *Server) IssueGraphImport(ctx context.Context, id string, in GraphImportRequest) (*GraphImportGrant, error) {
	if (in.Purpose != "" && in.Purpose != "node_result") || !job.Ref(in.ID) || len(in.ID) > 64 || !job.Ref(in.NodeID) || !job.ValidHash(in.ViewHash) || !job.ValidHash(in.TargetPlanHash) || len(in.RecordIDs) < 1 || len(in.RecordIDs) > 32 || in.ExpectedRevision < 0 {
		return nil, status.Error(codes.InvalidArgument, "IMPORT_INVALID")
	}
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, importFailure("NOT_GRAPH")
	}
	var out *GraphImportGrant
	e = s.db.Tx(ctx, func(q store.Query) error {
		actor, e := graphMutationAuthorityTx(ctx, q, j, "goals:import")
		if e != nil {
			return e
		}
		prior, _, _, e := readImportGrantTx(ctx, q, j, in.ID)
		if e == nil {
			if !bytes.Equal(job.JSON(prior.Request), job.JSON(in)) {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			out = prior
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var active, deadline int64
		var state string
		if e = q.QueryRowContext(ctx, `SELECT active_graph_generation,deadline,state FROM goals WHERE id=?`, j.graph.GoalID).Scan(&active, &deadline, &state); e != nil {
			return e
		}
		if in.TargetGeneration != active+1 || j.graph.Generation > active || !terminal(j.State) || j.State == "CANCELED" || terminal(state) {
			return importFailure("TARGET_STATE")
		}
		if in.ExpiresAt <= store.Now() || in.ExpiresAt > deadline {
			return importFailure("EXPIRY")
		}
		var count int
		if e = q.QueryRowContext(ctx, `SELECT count(*) FROM iu_import_grants WHERE goal_id=?`, j.graph.GoalID).Scan(&count); e != nil {
			return e
		}
		if count >= 64 {
			return importFailure("GRANT_LIMIT")
		}
		if _, e = checkImportSourceTx(ctx, q, j, in); e != nil {
			return e
		}
		artifacts, e := collectImportArtifactsTx(ctx, q, j.graph)
		if e != nil {
			return e
		}
		p, _ := rpcutil.PrincipalFrom(ctx)
		if _, e = q.ExecContext(ctx, `INSERT INTO iu_import_grants VALUES(?,?,?,?,?,?,?,?)`, j.graph.GoalID, in.ID, j.ID, job.JSON(in), actor, p.ManagedKeyDigest, job.JSON(artifacts), store.Now()); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO iu_import_grant_state(goal_id,grant_id,state,expires_at) VALUES(?,?,'ISSUED',?)`, j.graph.GoalID, in.ID, in.ExpiresAt); e != nil {
			return e
		}
		for _, a := range artifacts {
			if e = pinArtifactRef(ctx, q, a, "iu_memory", importGrantRef(j.graph.GoalID, in.ID)); e != nil {
				return e
			}
		}
		if e = appendJobEvent(ctx, q, j.ID, "graph.import_grant_issued", map[string]any{"grant_id": in.ID, "target_generation": in.TargetGeneration, "actor": actor}, "", ""); e != nil {
			return e
		}
		out, _, _, e = readImportGrantTx(ctx, q, j, in.ID)
		return e
	})
	return out, e
}
func (s *Server) GraphImport(ctx context.Context, id, grant string) (*GraphImportGrant, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, importFailure("NOT_GRAPH")
	}
	var out *GraphImportGrant
	e = s.db.Tx(ctx, func(q store.Query) error {
		var err error
		out, _, _, err = readImportGrantTx(ctx, q, j, grant)
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT body FROM iu_import_snapshots WHERE goal_id=? AND grant_id=? ORDER BY generation,node_id`, j.graph.GoalID, grant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var snap GraphImportSnapshot
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &snap); err != nil {
				return err
			}
			out.Snapshots = append(out.Snapshots, snap)
		}
		return rows.Err()
	})
	return out, e
}
func (s *Server) RevokeGraphImport(ctx context.Context, id, grant string) error {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return e
	}
	if j.graph == nil {
		return importFailure("NOT_GRAPH")
	}
	return s.db.Tx(ctx, func(q store.Query) error {
		actor, e := graphMutationAuthorityTx(ctx, q, j, "goals:import")
		if e != nil {
			return e
		}
		g, _, _, e := readImportGrantTx(ctx, q, j, grant)
		if e != nil {
			return e
		}
		if g.State == "CONSUMED" {
			return importFailure("GRANT_CONSUMED_USE_INVALIDATION")
		}
		if g.State != "ISSUED" {
			return nil
		}
		if _, e = q.ExecContext(ctx, `UPDATE iu_import_grant_state SET state='REVOKED' WHERE goal_id=? AND grant_id=?`, j.graph.GoalID, grant); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `DELETE FROM artifact_refs WHERE ref_type='iu_memory' AND ref_id=?`, importGrantRef(j.graph.GoalID, grant)); e != nil {
			return e
		}
		return appendJobEvent(ctx, q, id, "graph.import_grant_revoked", map[string]any{"grant_id": grant, "actor": actor}, "", "")
	})
}
func checkImportIssuerTx(ctx context.Context, q store.Query, j *Job, digest string) error {
	if digest == "" {
		return nil
	} // static config identities are fixed for this Server process
	var owner string
	var projects, scopes []byte
	e := q.QueryRowContext(ctx, `SELECT owner,projects_json,scopes_json FROM api_keys WHERE digest=? AND revoked_at=0 AND expires_at>?`, digest, store.Now()).Scan(&owner, &projects, &scopes)
	if e != nil {
		return importFailure("ISSUER_REVOKED")
	}
	var ps, ss []string
	if json.Unmarshal(projects, &ps) != nil || json.Unmarshal(scopes, &ss) != nil || owner != j.owner || !config.Contains(ps, j.project) || !config.Contains(ss, "goals:import") || !config.Contains(ss, "jobs:read") {
		return importFailure("ISSUER_REVOKED")
	}
	return nil
}

// Conservative compatibility: every actually read source and frozen execution
// binding must survive. An unchecked model claim cannot narrow this dependency.
func importCompatible(ctx context.Context, q store.Query, source *Job, in GraphImportRequest, target *frozenGraph, nodeID string) error {
	a, ok := graphNode(source.graph, in.NodeID)
	if !ok {
		return importFailure("SOURCE_NODE")
	}
	b, ok := graphNode(target, nodeID)
	if !ok {
		return importFailure("TARGET_NODE")
	}
	if !bytes.Equal(job.JSON(a), job.JSON(b)) || !bytes.Equal(job.JSON(graphOperator(source.graph, a)), job.JSON(graphOperator(target, b))) || !bytes.Equal(job.JSON(source.graph.Plan.Spec.Schemas), job.JSON(target.Plan.Spec.Schemas)) || !bytes.Equal(job.JSON(source.graph.Plan.Spec.Workspace), job.JSON(target.Plan.Spec.Workspace)) || !bytes.Equal(job.JSON(source.graph.Plan.Spec.Rules), job.JSON(target.Plan.Spec.Rules)) || !bytes.Equal(job.JSON(source.graph.Digests), job.JSON(target.Digests)) || !bytes.Equal(job.JSON(source.graph.Commands[a.Operator]), job.JSON(target.Commands[b.Operator])) {
		return importFailure("CONTEXT_CHANGED")
	}
	var raw []byte
	if e := q.QueryRowContext(ctx, `SELECT context_json FROM iu_nodes WHERE goal_id=? AND generation=? AND node_id=?`, source.graph.GoalID, source.graph.Generation, a.ID).Scan(&raw); e != nil {
		return e
	}
	var manifest graph.ContextManifest
	if e := json.Unmarshal(raw, &manifest); e != nil {
		return importFailure("CONTEXT_MISSING")
	}
	if manifest.Version != "iu-context-v1" || manifest.SHA256 != graph.Hash([]byte(manifest.Text)) {
		return importFailure("CONTEXT_MISSING")
	}
	var body struct {
		Inputs []graph.InputValue `json:"inputs"`
	}
	if json.Unmarshal([]byte(manifest.Text), &body) != nil || len(body.Inputs) > 0 {
		return importFailure("ARTIFACT_INPUT_REQUIRES_NEW_PROOF")
	}
	sources := map[string]graph.Source{}
	for _, src := range target.Plan.Spec.Sources {
		sources[src.ID] = src
	}
	view, e := graphMemoryTx(ctx, q, source.graph, in.NodeID)
	if e != nil {
		return e
	}
	selected := map[string]bool{}
	for _, id := range in.RecordIDs {
		selected[id] = true
	}
	for _, r := range view.Records {
		if selected[r.ID] {
			for _, ref := range r.Sources {
				src, ok := sources[ref.ID]
				if !ok || graph.Hash([]byte(src.Text)) != ref.SHA256 {
					return importFailure("RECORD_SOURCE_CHANGED")
				}
			}
		}
	}
	for _, span := range manifest.Spans {
		src, ok := sources[span.Source]
		if !ok || graph.Hash([]byte(src.Text)) != span.SHA256 {
			return importFailure("SOURCE_CHANGED")
		}
	}
	return nil
}
func bindGraphImportsTx(ctx context.Context, q store.Query, target *Job) error {
	f := target.graph
	bindings := append([]graph.ImportBinding{}, f.Plan.Spec.Imports...)
	reuse := map[string]bool{}
	for _, b := range f.Plan.Spec.Reuse {
		bindings = append(bindings, graph.ImportBinding{GrantID: b.GrantID, NodeIDs: []string{b.NodeID}})
		reuse[b.GrantID] = true
	}
	for _, binding := range bindings {
		var sourceID string
		if e := q.QueryRowContext(ctx, `SELECT source_job FROM iu_import_grants WHERE goal_id=? AND grant_id=?`, f.GoalID, binding.GrantID).Scan(&sourceID); e != nil {
			return importFailure("GRANT_MISSING")
		}
		source, e := readJob(ctx, q, sourceID)
		if e != nil {
			return e
		}
		if source.graph == nil || source.graph.GoalID != f.GoalID || source.project != target.project || source.owner != target.owner || source.graph.Generation >= f.Generation {
			return importFailure("NAMESPACE")
		}
		grant, issuer, artifacts, e := readImportGrantTx(ctx, q, source, binding.GrantID)
		if e != nil {
			return e
		}
		in := grant.Request
		if reuse[binding.GrantID] != (in.Purpose == "node_result") {
			return importFailure("GRANT_PURPOSE")
		}
		var expires int64
		if e = q.QueryRowContext(ctx, `SELECT expires_at FROM iu_import_grant_state WHERE goal_id=? AND grant_id=?`, f.GoalID, in.ID).Scan(&expires); e != nil {
			return e
		}
		if grant.State != "ISSUED" || expires <= store.Now() || in.ExpiresAt <= store.Now() || in.TargetGeneration != f.Generation || in.TargetPlanHash != f.Plan.Digest {
			return importFailure("GRANT_NOT_USABLE")
		}
		if e = checkImportIssuerTx(ctx, q, source, issuer); e != nil {
			return e
		}
		view, e := checkImportSourceTx(ctx, q, source, in)
		if e != nil {
			return e
		}
		for _, nodeID := range binding.NodeIDs {
			if reuse[binding.GrantID] {
				if e = freezeReuseOfferTx(ctx, q, source, target, in, nodeID, view, artifacts); e != nil {
					return e
				}
				continue
			}
			if e = importCompatible(ctx, q, source, in, f, nodeID); e != nil {
				return e
			}
			memory, e := graph.ImportView(view, in.RecordIDs, fmt.Sprintf("%s/%d/%s/%s", f.GoalID, f.Generation, nodeID, in.ID))
			if e != nil {
				return e
			}
			snap := GraphImportSnapshot{GrantID: in.ID, SourceJob: source.ID, SourceGoal: f.GoalID, SourceGeneration: source.graph.Generation, SourceNode: in.NodeID, SourceRevision: in.ExpectedRevision, Memory: memory, TargetGeneration: f.Generation, TargetNode: nodeID}
			if _, e = q.ExecContext(ctx, `INSERT INTO iu_import_snapshots VALUES(?,?,?,?,?,?)`, f.GoalID, f.Generation, nodeID, in.ID, job.JSON(snap), job.JSON(artifacts)); e != nil {
				return e
			}
			for _, a := range artifacts {
				if e = pinArtifactRef(ctx, q, a, "iu_memory", fmt.Sprintf("import/%s/%d/%s/%s", f.GoalID, f.Generation, nodeID, in.ID)); e != nil {
					return e
				}
			}
		}
		if _, e = q.ExecContext(ctx, `UPDATE iu_import_grant_state SET state='CONSUMED',target_job=? WHERE goal_id=? AND grant_id=? AND state='ISSUED'`, target.ID, f.GoalID, in.ID); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `DELETE FROM artifact_refs WHERE ref_type='iu_memory' AND ref_id=?`, importGrantRef(f.GoalID, in.ID)); e != nil {
			return e
		}
		if e = appendJobEvent(ctx, q, target.ID, "graph.import_consumed", map[string]any{"grant_id": in.ID, "source_job": source.ID, "source_revision": in.ExpectedRevision}, "", ""); e != nil {
			return e
		}
	}
	return nil
}
func importViewsTx(ctx context.Context, q store.Query, f *frozenGraph, node string) ([]graph.MemoryView, error) {
	var views []graph.MemoryView
	rows, e := q.QueryContext(ctx, `SELECT body FROM iu_import_snapshots WHERE goal_id=? AND generation=? AND node_id=? ORDER BY grant_id`, f.GoalID, f.Generation, node)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var raw []byte
		var snap GraphImportSnapshot
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal(raw, &snap); e != nil {
			rows.Close()
			return nil, e
		}
		views = append(views, snap.Memory.View)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	expected := 0
	for _, b := range f.Plan.Spec.Imports {
		for _, id := range b.NodeIDs {
			if id == node {
				expected++
			}
		}
	}
	if expected != len(views) {
		return nil, importFailure("SNAPSHOT_MISSING")
	}
	return views, nil
}
func (s *Server) expireGraphImports(ctx context.Context) error {
	return s.db.Tx(ctx, func(q store.Query) error {
		rows, e := q.QueryContext(ctx, `SELECT goal_id,grant_id FROM iu_import_grant_state WHERE state='ISSUED' AND expires_at<=? LIMIT 16`, store.Now())
		if e != nil {
			return e
		}
		var refs [][2]string
		for rows.Next() {
			var r [2]string
			if e = rows.Scan(&r[0], &r[1]); e != nil {
				rows.Close()
				return e
			}
			refs = append(refs, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, r := range refs {
			if _, e = q.ExecContext(ctx, `UPDATE iu_import_grant_state SET state='EXPIRED' WHERE goal_id=? AND grant_id=? AND state='ISSUED'`, r[0], r[1]); e != nil {
				return e
			}
			if _, e = q.ExecContext(ctx, `DELETE FROM artifact_refs WHERE ref_type='iu_memory' AND ref_id=?`, importGrantRef(r[0], r[1])); e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Server) httpIssueGraphImport(w http.ResponseWriter, r *http.Request) {
	raw, e := readJSONBody(w, r, 16<<10)
	if e != nil {
		httpError(w, e)
		return
	}
	var in GraphImportRequest
	if e = jsonutil.Decode(raw, &in); e != nil {
		httpError(w, status.Error(codes.InvalidArgument, "IMPORT_INVALID"))
		return
	}
	out, e := s.IssueGraphImport(r.Context(), r.PathValue("id"), in)
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
func (s *Server) httpGraphImport(w http.ResponseWriter, r *http.Request) {
	out, e := s.GraphImport(r.Context(), r.PathValue("id"), r.PathValue("grant"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
func (s *Server) httpRevokeGraphImport(w http.ResponseWriter, r *http.Request) {
	if e := s.RevokeGraphImport(r.Context(), r.PathValue("id"), r.PathValue("grant")); e != nil {
		httpError(w, e)
		return
	}
	s.httpGraphImport(w, r)
}
