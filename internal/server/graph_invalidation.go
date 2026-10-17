package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GraphValidityEvent struct {
	Event    graph.InvalidationEvent `json:"event"`
	Revision int64                   `json:"revision"`
	Actor    string                  `json:"actor"`
	State    string                  `json:"state"`
}
type GraphValidityStatus struct {
	Revision int64                `json:"revision"`
	Events   []GraphValidityEvent `json:"events"`
}

func graphValidityRevisionTx(ctx context.Context, q store.Query, f *frozenGraph) (int64, error) {
	var revision int64
	e := q.QueryRowContext(ctx, `SELECT coalesce((SELECT revision FROM iu_validity_revisions WHERE goal_id=? AND generation=?),0)`, f.GoalID, f.Generation).Scan(&revision)
	return revision, e
}
func graphInvalidatedTx(ctx context.Context, q store.Query, f *frozenGraph) (bool, error) {
	var count int
	e := q.QueryRowContext(ctx, `SELECT count(*) FROM iu_validity_events WHERE goal_id=? AND generation=? AND audit_only=0`, f.GoalID, f.Generation).Scan(&count)
	return count > 0, e
}

// Recheck managed credentials under the same SQLite transaction as the write.
func graphMutationAuthorityTx(ctx context.Context, q store.Query, j *Job, scope string) (string, error) {
	p, e := rpcutil.Require(ctx, scope, false)
	if e != nil {
		return "", e
	}
	if _, e = rpcutil.Require(ctx, "jobs:read", false); e != nil {
		return "", e
	}
	if p.Identity.Owner != j.owner || !config.Contains(p.Identity.Projects, j.project) {
		return "", status.Error(codes.PermissionDenied, "GRAPH_AUTHORITY_REQUIRED")
	}
	if p.ManagedKeyDigest != "" {
		var owner, id string
		var projects, scopes []byte
		e = q.QueryRowContext(ctx, `SELECT id,owner,projects_json,scopes_json FROM api_keys WHERE digest=? AND revoked_at=0 AND expires_at>?`, p.ManagedKeyDigest, store.Now()).Scan(&id, &owner, &projects, &scopes)
		if e != nil {
			return "", status.Error(codes.PermissionDenied, "GRAPH_AUTHORITY_REVOKED")
		}
		var ps, ss []string
		if json.Unmarshal(projects, &ps) != nil || json.Unmarshal(scopes, &ss) != nil || owner != j.owner || !config.Contains(ps, j.project) || !config.Contains(ss, scope) || !config.Contains(ss, "jobs:read") {
			return "", status.Error(codes.PermissionDenied, "GRAPH_AUTHORITY_REVOKED")
		}
		return owner + "/" + id, nil
	}
	return p.Identity.Owner, nil
}
func (s *Server) InvalidateGraph(ctx context.Context, id string, in graph.InvalidationEvent) error {
	if e := in.Validate(); e != nil {
		return status.Error(codes.InvalidArgument, e.Error())
	}
	if _, e := rpcutil.Require(ctx, "goals:invalidate", false); e != nil {
		return e
	}
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return e
	}
	if j.graph == nil {
		return status.Error(codes.InvalidArgument, "NOT_GRAPH_JOB")
	}
	return s.db.Tx(ctx, func(q store.Query) error { return invalidateGraphTx(ctx, q, j, in) })
}
func invalidateGraphTx(ctx context.Context, q store.Query, j *Job, in graph.InvalidationEvent) error {
	actor, e := graphMutationAuthorityTx(ctx, q, j, "goals:invalidate")
	if e != nil {
		return e
	}
	f := j.graph
	if in.GoalID != f.GoalID || in.Generation != f.Generation {
		return status.Error(codes.Aborted, "INVALIDATION_NAMESPACE_MISMATCH")
	}
	body := job.JSON(in)
	var previous []byte
	e = q.QueryRowContext(ctx, `SELECT body FROM iu_validity_events WHERE goal_id=? AND generation=? AND event_id=?`, f.GoalID, f.Generation, in.ID).Scan(&previous)
	if e == nil {
		if !bytes.Equal(previous, body) {
			return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	revision, e := graphValidityRevisionTx(ctx, q, f)
	if e != nil {
		return e
	}
	var generation int64
	var state string
	if e = q.QueryRowContext(ctx, `SELECT active_graph_generation,state FROM goals WHERE id=?`, f.GoalID).Scan(&generation, &state); e != nil {
		return e
	}
	if generation != f.Generation || revision != in.ExpectedRevision {
		return status.Error(codes.Aborted, "INVALIDATION_STALE")
	}
	ended := state == "SUCCEEDED" || state == "FAILED" || state == "CANCELED"
	if ended && !in.AuditOnly {
		return status.Error(codes.FailedPrecondition, "GOAL_TERMINAL_AUDIT_ONLY")
	}
	if !ended && in.AuditOnly {
		return status.Error(codes.InvalidArgument, "AUDIT_ONLY_REQUIRES_TERMINAL_GOAL")
	}
	if revision >= 64 {
		return status.Error(codes.ResourceExhausted, "INVALIDATION_EVENT_LIMIT")
	}
	for _, src := range in.Sources {
		found := false
		for _, original := range f.Plan.Spec.Sources {
			if original.ID == src.ID && graph.Hash([]byte(original.Text)) == src.SHA256 {
				found = true
			}
		}
		if !found {
			return status.Error(codes.InvalidArgument, "INVALIDATION_SOURCE_UNKNOWN")
		}
	}
	if len(in.Records) > 0 {
		known := map[string]bool{}
		rows, e := q.QueryContext(ctx, `SELECT input_view,output_view FROM iu_nodes WHERE goal_id=? AND generation=?`, f.GoalID, f.Generation)
		if e != nil {
			return e
		}
		for rows.Next() {
			var a, b []byte
			if e = rows.Scan(&a, &b); e != nil {
				rows.Close()
				return e
			}
			for _, raw := range [][]byte{a, b} {
				if len(raw) == 0 {
					continue
				}
				var v graph.MemoryView
				if e = json.Unmarshal(raw, &v); e != nil {
					rows.Close()
					return e
				}
				for _, r := range v.Records {
					known[r.ID] = true
				}
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, record := range in.Records {
			if !known[record] {
				return status.Error(codes.InvalidArgument, "INVALIDATION_RECORD_UNKNOWN")
			}
		}
	}
	if _, e = q.ExecContext(ctx, `INSERT INTO iu_validity_revisions(goal_id,generation,revision) VALUES(?,?,0) ON CONFLICT DO NOTHING`, f.GoalID, f.Generation); e != nil {
		return e
	}
	if _, e = q.ExecContext(ctx, `UPDATE iu_validity_revisions SET revision=revision+1 WHERE goal_id=? AND generation=? AND revision=?`, f.GoalID, f.Generation, revision); e != nil {
		return e
	}
	if _, e = q.ExecContext(ctx, `INSERT INTO iu_validity_events(goal_id,generation,event_id,revision,actor,body,audit_only,created) VALUES(?,?,?,?,?,?,?,?)`, f.GoalID, f.Generation, in.ID, revision+1, actor, body, in.AuditOnly, store.Now()); e != nil {
		return e
	}
	progress := "RECORDED"
	if in.AuditOnly {
		progress = "AUDIT_ONLY"
	}
	if _, e = q.ExecContext(ctx, `INSERT INTO iu_validity_progress VALUES(?,?,?,?)`, f.GoalID, f.Generation, in.ID, progress); e != nil {
		return e
	}
	return appendJobEvent(ctx, q, j.ID, "graph.invalidated", map[string]any{"event_id": in.ID, "validity_revision": revision + 1, "audit_only": in.AuditOnly, "evidence_sha256": in.EvidenceHash}, "", "")
}
func (s *Server) GraphValidity(ctx context.Context, id string) (*GraphValidityStatus, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, status.Error(codes.NotFound, "NOT_GRAPH_JOB")
	}
	out := &GraphValidityStatus{Events: []GraphValidityEvent{}}
	e = s.db.Tx(ctx, func(q store.Query) error {
		var e error
		out.Revision, e = graphValidityRevisionTx(ctx, q, j.graph)
		if e != nil {
			return e
		}
		rows, e := q.QueryContext(ctx, `SELECT e.body,e.revision,e.actor,p.state FROM iu_validity_events e JOIN iu_validity_progress p USING(goal_id,generation,event_id) WHERE e.goal_id=? AND e.generation=? ORDER BY e.revision`, j.graph.GoalID, j.graph.Generation)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v GraphValidityEvent
			var b []byte
			if e = rows.Scan(&b, &v.Revision, &v.Actor, &v.State); e != nil {
				return e
			}
			if e = json.Unmarshal(b, &v.Event); e != nil {
				return e
			}
			out.Events = append(out.Events, v)
		}
		return rows.Err()
	})
	return out, e
}
func (s *Server) reconcileGraphInvalidations(ctx context.Context) error {
	// Bound scheduler work. Each event has a separate durable progress record.
	rows, e := s.db.SQL.QueryContext(ctx, `SELECT x.job_id,e.event_id,e.revision FROM iu_validity_events e JOIN iu_validity_progress p USING(goal_id,generation,event_id) JOIN iu_graphs x USING(goal_id,generation) WHERE p.state='RECORDED' ORDER BY e.goal_id,e.generation,e.revision LIMIT 4`)
	if e != nil {
		return e
	}
	type pending struct {
		job, event string
		revision   int64
	}
	var queue []pending
	for rows.Next() {
		var item pending
		if e = rows.Scan(&item.job, &item.event, &item.revision); e != nil {
			rows.Close()
			return e
		}
		queue = append(queue, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, item := range queue {
		if e = s.db.Tx(ctx, func(q store.Query) error {
			j, e := readJob(ctx, q, item.job)
			if e != nil {
				return e
			}
			f := j.graph
			var raw []byte
			var state string
			if e = q.QueryRowContext(ctx, `SELECT e.body,p.state FROM iu_validity_events e JOIN iu_validity_progress p USING(goal_id,generation,event_id) WHERE e.goal_id=? AND e.generation=? AND e.event_id=?`, f.GoalID, f.Generation, item.event).Scan(&raw, &state); e != nil {
				return e
			}
			if state != "RECORDED" {
				return nil
			}
			var in graph.InvalidationEvent
			if e = json.Unmarshal(raw, &in); e != nil {
				return e
			}
			nodes, e := readGraphNodes(ctx, q, f)
			if e != nil {
				return e
			}
			for _, node := range nodes {
				original := node.View
				if len(original) == 0 {
					original = node.InputView
				}
				if len(original) == 0 {
					continue
				}
				var view graph.MemoryView
				if e = json.Unmarshal(original, &view); e != nil {
					return e
				}
				originalHash := view.SHA256
				var previous []byte
				readErr := q.QueryRowContext(ctx, `SELECT body FROM iu_validity_views WHERE goal_id=? AND generation=? AND node_id=? AND revision<? ORDER BY revision DESC LIMIT 1`, f.GoalID, f.Generation, node.ID, item.revision).Scan(&previous)
				if readErr == nil {
					if e = json.Unmarshal(previous, &view); e != nil {
						return e
					}
				} else if !errors.Is(readErr, sql.ErrNoRows) {
					return readErr
				}
				view, e = graph.ProjectInvalidation(view, in)
				if e != nil {
					return e
				}
				if _, e = q.ExecContext(ctx, `INSERT INTO iu_validity_views VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, f.GoalID, f.Generation, node.ID, item.revision, originalHash, job.JSON(view)); e != nil {
					return e
				}
			}
			_, e = q.ExecContext(ctx, `UPDATE iu_validity_progress SET state='APPLIED' WHERE goal_id=? AND generation=? AND event_id=? AND state='RECORDED'`, f.GoalID, f.Generation, item.event)
			return e
		}); e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) GraphMemory(ctx context.Context, id, node string) (graph.MemoryView, error) {
	var view graph.MemoryView
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return view, e
	}
	if j.graph == nil {
		return view, status.Error(codes.NotFound, "NOT_GRAPH_JOB")
	}
	e = s.db.Tx(ctx, func(q store.Query) error {
		f := j.graph
		var revision int64
		if e := q.QueryRowContext(ctx, `SELECT coalesce(max(revision),0) FROM iu_validity_events WHERE goal_id=? AND generation=? AND audit_only=0`, f.GoalID, f.Generation).Scan(&revision); e != nil {
			return e
		}
		var raw []byte
		if revision > 0 {
			if e := q.QueryRowContext(ctx, `SELECT body FROM iu_validity_views WHERE goal_id=? AND generation=? AND node_id=? AND revision=?`, f.GoalID, f.Generation, node, revision).Scan(&raw); e != nil {
				if errors.Is(e, sql.ErrNoRows) {
					return status.Error(codes.FailedPrecondition, "VALIDITY_PROJECTION_PENDING")
				}
				return e
			}
		} else {
			if e := q.QueryRowContext(ctx, `SELECT coalesce(output_view,input_view) FROM iu_nodes WHERE goal_id=? AND generation=? AND node_id=?`, f.GoalID, f.Generation, node).Scan(&raw); e != nil {
				return e
			}
		}
		if len(raw) == 0 {
			return status.Error(codes.NotFound, "MEMORY_NOT_AVAILABLE")
		}
		return json.Unmarshal(raw, &view)
	})
	return view, e
}
func (s *Server) httpInvalidateGraph(w http.ResponseWriter, r *http.Request) {
	raw, e := readJSONBody(w, r, 16<<10)
	if e != nil {
		httpError(w, e)
		return
	}
	var in graph.InvalidationEvent
	if e = jsonutil.Decode(raw, &in); e != nil {
		httpError(w, status.Error(codes.InvalidArgument, "INVALIDATION_INVALID"))
		return
	}
	if e = s.InvalidateGraph(r.Context(), r.PathValue("id"), in); e != nil {
		httpError(w, e)
		return
	}
	out, e := s.GraphValidity(r.Context(), r.PathValue("id"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
func (s *Server) httpGraphValidity(w http.ResponseWriter, r *http.Request) {
	out, e := s.GraphValidity(r.Context(), r.PathValue("id"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
func (s *Server) httpGraphMemory(w http.ResponseWriter, r *http.Request) {
	out, e := s.GraphMemory(r.Context(), r.PathValue("id"), r.PathValue("node"))
	if e != nil {
		httpError(w, e)
		return
	}
	if len(job.JSON(out)) > 1<<20 {
		httpError(w, status.Error(codes.ResourceExhausted, fmt.Sprint("MEMORY_RESPONSE_LIMIT")))
		return
	}
	jsonResponse(w, 200, out)
}
