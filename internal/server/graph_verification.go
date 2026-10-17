package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"time"
)

type GraphRecordVerificationRequest struct {
	ID               string `json:"id"`
	NodeID           string `json:"node_id"`
	RecordID         string `json:"record_id"`
	RecordHash       string `json:"record_sha256"`
	ViewHash         string `json:"view_sha256"`
	ExpectedRevision int64  `json:"expected_revision"`
	Profile          string `json:"profile"`
	ProfileVersion   string `json:"profile_version"`
	SourceID         string `json:"source_id"`
}
type GraphRecordVerificationReceipt struct {
	ProfileDefinition graph.RecordVerifierProfile    `json:"verifier_profile"`
	Version           string                         `json:"version"`
	GoalID            string                         `json:"goal_id"`
	Generation        int64                          `json:"graph_generation"`
	Request           GraphRecordVerificationRequest `json:"request"`
	ProfileHash       string                         `json:"profile_sha256"`
	Scope             string                         `json:"scope"`
	Source            graph.SourceRef                `json:"source"`
	graph.RecordVerificationResult
	ElapsedMicros int64 `json:"elapsed_us"`
}
type GraphVerificationCoverage struct {
	Receipt GraphRecordVerificationReceipt `json:"receipt"`
	Applies bool                           `json:"applies"`
}
type VerifiedGraphMemoryView struct {
	Version          string                      `json:"version"`
	Scope            string                      `json:"scope"`
	OriginalViewHash string                      `json:"original_view_sha256"`
	View             graph.MemoryView            `json:"view"`
	Coverage         []GraphVerificationCoverage `json:"coverage"`
}

func verificationRecord(view graph.MemoryView, id string) (graph.MemoryRecord, error) {
	for _, c := range view.Conflicts {
		if c.RecordID == id {
			return graph.MemoryRecord{}, importFailure("VERIFICATION_CONFLICT")
		}
	}
	for _, r := range view.Records {
		if r.ID == id {
			return r, nil
		}
	}
	return graph.MemoryRecord{}, status.Error(codes.NotFound, "VERIFICATION_RECORD_UNKNOWN")
}
func (s *Server) VerifyGraphRecord(ctx context.Context, id string, in GraphRecordVerificationRequest) (*GraphRecordVerificationReceipt, error) {
	if !job.ValidKey(in.ID) || !job.Ref(in.NodeID) || !job.Ref(in.RecordID) || !job.ValidHash(in.RecordHash) || !job.ValidHash(in.ViewHash) || !job.Ref(in.Profile) || !job.Ref(in.ProfileVersion) || !job.Ref(in.SourceID) || in.ExpectedRevision < 0 {
		return nil, status.Error(codes.InvalidArgument, "VERIFICATION_INVALID")
	}
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, importFailure("NOT_GRAPH")
	}
	f := j.graph
	var out *GraphRecordVerificationReceipt
	e = s.db.Tx(ctx, func(q store.Query) error {
		actor, e := graphMutationAuthorityTx(ctx, q, j, "goals:verify")
		if e != nil {
			return e
		}
		rev, e := graphValidityRevisionTx(ctx, q, f)
		if e != nil {
			return e
		}
		if rev != in.ExpectedRevision {
			return status.Error(codes.Aborted, "VERIFICATION_STALE")
		}
		var active int64
		if e = q.QueryRowContext(ctx, `SELECT active_graph_generation FROM goals WHERE id=?`, f.GoalID).Scan(&active); e != nil {
			return e
		}
		if active != f.Generation {
			return status.Error(codes.Aborted, "VERIFICATION_STALE_GENERATION")
		}
		view, e := graphMemoryTx(ctx, q, f, in.NodeID)
		if e != nil {
			return e
		}
		if view.SHA256 != in.ViewHash {
			return importFailure("VERIFICATION_VIEW_CHANGED")
		}
		record, e := verificationRecord(view, in.RecordID)
		if e != nil {
			return e
		}
		if graph.Hash(job.JSON(record)) != in.RecordHash || record.Validity != "VALID" {
			return importFailure("VERIFICATION_RECORD_CHANGED")
		}
		profile, ok := s.cfg.Jobs.GraphRecordVerifiers[in.Profile]
		if !ok || !profile.Valid() || profile.Version != in.ProfileVersion {
			return importFailure("VERIFIER_PROFILE_UNAVAILABLE")
		}
		var request, body []byte
		e = q.QueryRowContext(ctx, `SELECT request,body FROM iu_record_verifications WHERE goal_id=? AND generation=? AND node_id=? AND verification_id=?`, f.GoalID, f.Generation, in.NodeID, in.ID).Scan(&request, &body)
		if e == nil {
			if !bytes.Equal(request, job.JSON(in)) {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			if e = json.Unmarshal(body, &out); e != nil {
				return e
			}
			if out.ProfileHash != graph.Hash(job.JSON(profile)) {
				return importFailure("VERIFIER_PROFILE_CHANGED")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var count int
		if e = q.QueryRowContext(ctx, `SELECT count(*) FROM iu_record_verifications WHERE goal_id=? AND generation=?`, f.GoalID, f.Generation).Scan(&count); e != nil {
			return e
		}
		if count >= 128 {
			return status.Error(codes.ResourceExhausted, "VERIFICATION_LIMIT")
		}
		var source graph.Source
		found := false
		for _, ref := range record.Sources {
			if ref.ID == in.SourceID {
				for _, src := range f.Plan.Spec.Sources {
					if src.ID == ref.ID && graph.Hash([]byte(src.Text)) == ref.SHA256 {
						source = src
						source.SHA256 = ref.SHA256
						found = true
					}
				}
			}
		}
		if !found {
			return importFailure("VERIFICATION_SOURCE_MISSING")
		}
		profile.RecordKinds = append([]string{}, profile.RecordKinds...)
		start := time.Now()
		verifyCtx, cancel := context.WithTimeout(ctx, time.Duration(profile.TimeoutMS)*time.Millisecond)
		result := graph.RunRecordVerifier(verifyCtx, profile, source, record)
		cancel()
		out = &GraphRecordVerificationReceipt{Version: "iu-record-verification-v1", ProfileDefinition: profile, GoalID: f.GoalID, Generation: f.Generation, Request: in, ProfileHash: graph.Hash(job.JSON(profile)), Scope: profile.Scope, Source: graph.SourceRef{ID: source.ID, SHA256: source.SHA256}, RecordVerificationResult: result, ElapsedMicros: time.Since(start).Microseconds()}
		artifacts, e := collectImportArtifactsTx(ctx, q, f)
		if e != nil {
			return e
		}
		for _, a := range artifacts {
			if e = pinArtifactRef(ctx, q, a, "iu_memory", fmt.Sprintf("verification/%s/%d/%s/%s", f.GoalID, f.Generation, in.NodeID, in.ID)); e != nil {
				return e
			}
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO iu_record_verifications VALUES(?,?,?,?,?,?,?,?,?,?)`, f.GoalID, f.Generation, in.NodeID, in.ID, in.RecordID, profile.Scope, job.JSON(in), job.JSON(out), actor, store.Now()); e != nil {
			return e
		}
		return appendJobEvent(ctx, q, id, "graph.record_verified", map[string]any{"verification_id": in.ID, "node_id": in.NodeID, "record_id": in.RecordID, "scope": profile.Scope, "verdict": out.Verdict, "evidence_sha256": out.EvidenceHash}, "", "")
	})
	return out, e
}
func (s *Server) VerifiedGraphMemory(ctx context.Context, id, node, scope string) (*VerifiedGraphMemoryView, error) {
	if !job.Ref(scope) {
		return nil, status.Error(codes.InvalidArgument, "VERIFICATION_SCOPE_REQUIRED")
	}
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, importFailure("NOT_GRAPH")
	}
	f := j.graph
	out := &VerifiedGraphMemoryView{Version: "iu-verified-memory-v1", Scope: scope, Coverage: []GraphVerificationCoverage{}}
	e = s.db.Tx(ctx, func(q store.Query) error {
		original, e := graphMemoryTx(ctx, q, f, node)
		if e != nil {
			return e
		}
		out.OriginalViewHash = original.SHA256
		revision, e := graphValidityRevisionTx(ctx, q, f)
		if e != nil {
			return e
		}
		var active int64
		if e = q.QueryRowContext(ctx, `SELECT active_graph_generation FROM goals WHERE id=?`, f.GoalID).Scan(&active); e != nil {
			return e
		}
		rows, e := q.QueryContext(ctx, `SELECT body FROM iu_record_verifications WHERE goal_id=? AND generation=? AND node_id=? AND scope=? ORDER BY created,verification_id`, f.GoalID, f.Generation, node, scope)
		if e != nil {
			return e
		}
		covered := map[string]bool{}
		for rows.Next() {
			var raw []byte
			var receipt GraphRecordVerificationReceipt
			if e = rows.Scan(&raw); e != nil {
				rows.Close()
				return e
			}
			if e = json.Unmarshal(raw, &receipt); e != nil {
				rows.Close()
				return e
			}
			r, err := verificationRecord(original, receipt.Request.RecordID)
			p, registered := s.cfg.Jobs.GraphRecordVerifiers[receipt.Request.Profile]
			applies := err == nil && registered && p.Valid() && receipt.ProfileHash == graph.Hash(job.JSON(p)) && receipt.Request.ExpectedRevision == revision && receipt.Request.ViewHash == original.SHA256 && receipt.Request.RecordHash == graph.Hash(job.JSON(r)) && r.Validity == "VALID" && f.Generation == active
			if applies && receipt.Verdict == "PASS" {
				covered[r.ID] = true
			}
			out.Coverage = append(out.Coverage, GraphVerificationCoverage{receipt, applies})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		ids := []string{}
		for id := range covered {
			ids = append(ids, id)
		}
		out.View, e = graph.ProjectVerifiedScope(original, ids)
		if e != nil {
			return e
		}
		return nil
	})
	return out, e
}
func (s *Server) httpVerifyGraphRecord(w http.ResponseWriter, r *http.Request) {
	raw, e := readJSONBody(w, r, 8192)
	if e != nil {
		httpError(w, e)
		return
	}
	var in GraphRecordVerificationRequest
	if e = jsonutil.Decode(raw, &in); e != nil {
		httpError(w, status.Error(codes.InvalidArgument, "VERIFICATION_INVALID"))
		return
	}
	out, e := s.VerifyGraphRecord(r.Context(), r.PathValue("id"), in)
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
func (s *Server) httpVerifiedGraphMemory(w http.ResponseWriter, r *http.Request) {
	out, e := s.VerifiedGraphMemory(r.Context(), r.PathValue("id"), r.PathValue("node"), r.URL.Query().Get("scope"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}

type GraphRecordProofRef struct {
	NodeID string `json:"node_id"`
	ID     string `json:"verification_id"`
	Hash   string `json:"receipt_sha256"`
	Scope  string `json:"scope"`
}

func (s *Server) validateRecordProofsTx(ctx context.Context, q store.Query, f *frozenGraph, in GraphEvaluation) error {
	if len(in.RecordProofs) > 32 {
		return status.Error(codes.InvalidArgument, "RECORD_PROOF_LIMIT")
	}
	seen := map[string]bool{}
	for _, ref := range in.RecordProofs {
		key := ref.NodeID + "/" + ref.ID
		if !job.Ref(ref.NodeID) || !job.ValidKey(ref.ID) || !job.ValidHash(ref.Hash) || !job.Ref(ref.Scope) || seen[key] {
			return status.Error(codes.InvalidArgument, "RECORD_PROOF_INVALID")
		}
		seen[key] = true
		var raw []byte
		if e := q.QueryRowContext(ctx, `SELECT body FROM iu_record_verifications WHERE goal_id=? AND generation=? AND node_id=? AND verification_id=?`, f.GoalID, f.Generation, ref.NodeID, ref.ID).Scan(&raw); e != nil {
			return importFailure("RECORD_PROOF_MISSING")
		}
		if graph.Hash(raw) != ref.Hash {
			return importFailure("RECORD_PROOF_HASH")
		}
		var receipt GraphRecordVerificationReceipt
		if e := json.Unmarshal(raw, &receipt); e != nil {
			return e
		}
		p, ok := s.cfg.Jobs.GraphRecordVerifiers[receipt.Request.Profile]
		if !ok || !p.Valid() || receipt.ProfileHash != graph.Hash(job.JSON(p)) || receipt.Scope != ref.Scope || receipt.Request.ExpectedRevision != in.ExpectedValidityRevision || in.Verdict == "PASS" && receipt.Verdict != "PASS" {
			return importFailure("RECORD_PROOF_STALE")
		}
		view, e := graphMemoryTx(ctx, q, f, ref.NodeID)
		if e != nil {
			return e
		}
		r, e := verificationRecord(view, receipt.Request.RecordID)
		if e != nil || view.SHA256 != receipt.Request.ViewHash || r.Validity != "VALID" || graph.Hash(job.JSON(r)) != receipt.Request.RecordHash {
			return importFailure("RECORD_PROOF_STALE")
		}
	}
	return nil
}
