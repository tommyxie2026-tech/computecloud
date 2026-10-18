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
	"path/filepath"
	"time"
)

type GraphCoverageRequest struct {
	ID                       string `json:"id"`
	ExpectedValidityRevision int64  `json:"expected_validity_revision"`
	OutputHash               string `json:"output_sha256"`
	Profile                  string `json:"profile"`
	ProfileVersion           string `json:"profile_version"`
}
type GraphCoverageProofRef struct {
	ReceiptID string `json:"receipt_id"`
	SHA256    string `json:"sha256"`
}
type GraphCoverageReceipt struct {
	Version       string                        `json:"version"`
	GoalID        string                        `json:"goal_id"`
	Generation    int64                         `json:"graph_generation"`
	PlanHash      string                        `json:"plan_sha256"`
	CatalogHash   string                        `json:"catalog_sha256"`
	GoalSpecHash  string                        `json:"goal_spec_sha256"`
	Request       GraphCoverageRequest          `json:"request"`
	Profile       graph.CoverageVerifierProfile `json:"profile"`
	ProfileHash   string                        `json:"profile_sha256"`
	Sources       []graph.SourceRef             `json:"sources"`
	Artifacts     []string                      `json:"artifacts"`
	ElapsedMicros int64                         `json:"elapsed_us"`
	graph.CoverageResult
}
type coverageSnapshot struct {
	Values      map[string]json.RawMessage
	Artifacts   []string
	Fingerprint string
}

func coverageSnapshotTx(ctx context.Context, q store.Query, j *Job, in GraphCoverageRequest) (coverageSnapshot, error) {
	var out coverageSnapshot
	f := j.graph
	if f == nil || f.Plan.Spec.Version != graph.CoverageVersion || f.Plan.Spec.Coverage == nil {
		return out, importFailure("COVERAGE_GRAPH_REQUIRED")
	}
	var gen, deadline int64
	var state, hash string
	if e := q.QueryRowContext(ctx, `SELECT g.active_graph_generation,g.state,x.output_hash,g.deadline FROM goals g JOIN iu_graphs x ON x.goal_id=g.id WHERE x.job_id=?`, j.ID).Scan(&gen, &state, &hash, &deadline); e != nil {
		return out, e
	}
	if deadline > 0 && deadline <= store.Now() || gen != f.Generation || state != "EVALUATING" && state != "REPLAN_GUARDING" || j.State != "SUCCEEDED" || hash != in.OutputHash {
		return out, importFailure("COVERAGE_OUTPUT_STALE")
	}
	rev, e := graphValidityRevisionTx(ctx, q, f)
	if e != nil {
		return out, e
	}
	invalid, e := graphInvalidatedTx(ctx, q, f)
	if e != nil {
		return out, e
	}
	if rev != in.ExpectedValidityRevision || invalid {
		return out, importFailure("COVERAGE_VALIDITY_STALE")
	}
	out.Values = map[string]json.RawMessage{}
	for _, o := range f.Plan.Spec.Outputs {
		var state string
		var raw []byte
		if e = q.QueryRowContext(ctx, `SELECT state,output_json FROM iu_nodes WHERE goal_id=? AND generation=? AND node_id=?`, f.GoalID, f.Generation, o.Node).Scan(&state, &raw); e != nil {
			return out, e
		}
		if state != "SUCCEEDED" {
			return out, importFailure("COVERAGE_OUTPUT_MISSING")
		}
		var values map[string]json.RawMessage
		if e = json.Unmarshal(raw, &values); e != nil {
			return out, e
		}
		if len(values[o.Port]) == 0 {
			return out, importFailure("COVERAGE_OUTPUT_MISSING")
		}
		out.Values[o.Node+"/"+o.Port] = values[o.Port]
	}
	out.Artifacts, e = collectImportArtifactsTx(ctx, q, f)
	if e != nil {
		return out, e
	}
	if len(out.Artifacts) == 0 {
		return out, importFailure("COVERAGE_ARTIFACT_MISSING")
	}
	out.Fingerprint = graph.Hash(job.JSON(out.Values))
	return out, nil
}
func (s *Server) VerifyGraphCoverage(ctx context.Context, id string, in GraphCoverageRequest) (*GraphCoverageReceipt, error) {
	if !job.ValidKey(in.ID) || !job.ValidHash(in.OutputHash) || in.ExpectedValidityRevision < 0 || !job.Ref(in.Profile) || !job.Ref(in.ProfileVersion) {
		return nil, status.Error(codes.InvalidArgument, "COVERAGE_REQUEST_INVALID")
	}
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	f := j.graph
	if f == nil || f.Plan.Spec.Coverage == nil || f.CoverageVerifier == nil {
		return nil, importFailure("COVERAGE_GRAPH_REQUIRED")
	}
	c := f.Plan.Spec.Coverage
	if in.Profile != c.Profile || in.ProfileVersion != c.ProfileVersion {
		return nil, importFailure("COVERAGE_PROFILE_MISMATCH")
	}
	var snap coverageSnapshot
	var replay *GraphCoverageReceipt
	e = s.db.Tx(ctx, func(q store.Query) error {
		if _, e := graphMutationAuthorityTx(ctx, q, j, "goals:evaluate"); e != nil {
			return e
		}
		var request, body []byte
		e := q.QueryRowContext(ctx, `SELECT request,body FROM iu_coverage_receipts WHERE goal_id=? AND generation=? AND receipt_id=?`, f.GoalID, f.Generation, in.ID).Scan(&request, &body)
		if e == nil {
			if !bytes.Equal(request, job.JSON(in)) {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			return json.Unmarshal(body, &replay)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		snap, e = coverageSnapshotTx(ctx, q, j, in)
		return e
	})
	if e != nil || replay != nil {
		return replay, e
	}
	// Verify the immutable accepted bundles are still retrievable before issuing evidence.
	for _, a := range snap.Artifacts {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		var path, hash, state string
		var size int64
		if e = s.db.SQL.QueryRowContext(ctx, `SELECT path,hash,size,state FROM artifacts WHERE id=?`, a).Scan(&path, &hash, &size, &state); e != nil {
			return nil, e
		}
		if state != "ACCEPTED" {
			return nil, importFailure("COVERAGE_ARTIFACT_UNACCEPTED")
		}
		if _, e = readGraphBundle(filepath.Join(s.cfg.DataDir, "artifacts", path), size, hash); e != nil {
			return nil, importFailure("COVERAGE_ARTIFACT_UNAVAILABLE")
		}
	}
	p, ok := s.cfg.Jobs.GraphCoverageVerifiers[in.Profile]
	if !ok || !p.Valid() || graph.Hash(job.JSON(p)) != graph.Hash(job.JSON(f.CoverageVerifier)) {
		p = *f.CoverageVerifier
		p.Kind = "unavailable"
	}
	started := time.Now()
	result := graph.RunCoverageVerifier(ctx, p, f.Plan.Spec, snap.Values)
	out := &GraphCoverageReceipt{Version: "iu-coverage-receipt-v1", GoalID: f.GoalID, Generation: f.Generation, PlanHash: f.Plan.Digest, CatalogHash: c.SHA256, GoalSpecHash: c.GoalSpecHash, Request: in, Profile: p, ProfileHash: graph.Hash(job.JSON(p)), Artifacts: snap.Artifacts, ElapsedMicros: time.Since(started).Microseconds(), CoverageResult: result}
	for _, src := range f.Plan.Spec.Sources {
		out.Sources = append(out.Sources, graph.SourceRef{ID: src.ID, SHA256: src.SHA256})
	}
	e = s.db.Tx(ctx, func(q store.Query) error {
		actor, e := graphMutationAuthorityTx(ctx, q, j, "goals:evaluate")
		if e != nil {
			return e
		}
		current, e := readJob(ctx, q, id)
		if e != nil {
			return e
		}
		now, e := coverageSnapshotTx(ctx, q, current, in)
		if e != nil {
			return e
		}
		if now.Fingerprint != snap.Fingerprint || !bytes.Equal(job.JSON(now.Artifacts), job.JSON(snap.Artifacts)) {
			return importFailure("COVERAGE_EVIDENCE_CHANGED")
		}
		configured, ok := s.cfg.Jobs.GraphCoverageVerifiers[in.Profile]
		if result.Verdict != "UNKNOWN" && (!ok || graph.Hash(job.JSON(configured)) != out.ProfileHash) {
			return importFailure("COVERAGE_PROFILE_CHANGED")
		}
		var request, body []byte
		e = q.QueryRowContext(ctx, `SELECT request,body FROM iu_coverage_receipts WHERE goal_id=? AND generation=? AND receipt_id=?`, f.GoalID, f.Generation, in.ID).Scan(&request, &body)
		if e == nil {
			if !bytes.Equal(request, job.JSON(in)) {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			return json.Unmarshal(body, &out)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var n int
		if e = q.QueryRowContext(ctx, `SELECT count(*) FROM iu_coverage_receipts WHERE goal_id=? AND generation=?`, f.GoalID, f.Generation).Scan(&n); e != nil {
			return e
		}
		if n >= 128 {
			return status.Error(codes.ResourceExhausted, "COVERAGE_RECEIPT_LIMIT")
		}
		for _, a := range snap.Artifacts {
			if e = pinArtifactRef(ctx, q, a, "iu_memory", fmt.Sprintf("coverage/%s/%d/%s", f.GoalID, f.Generation, in.ID)); e != nil {
				return e
			}
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO iu_coverage_receipts VALUES(?,?,?,?,?,?,?,?)`, f.GoalID, f.Generation, in.ID, graph.Hash(job.JSON(in)), job.JSON(in), job.JSON(out), actor, store.Now()); e != nil {
			return e
		}
		return appendJobEvent(ctx, q, id, "graph.coverage_verified", map[string]any{"receipt_id": in.ID, "verdict": out.Verdict, "evidence_sha256": out.EvidenceHash}, "", "")
	})
	return out, e
}
func (s *Server) GraphCoverageReceipt(ctx context.Context, id, receiptID string) (*GraphCoverageReceipt, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, importFailure("COVERAGE_GRAPH_REQUIRED")
	}
	var raw []byte
	if e = s.db.SQL.QueryRowContext(ctx, `SELECT body FROM iu_coverage_receipts WHERE goal_id=? AND generation=? AND receipt_id=?`, j.graph.GoalID, j.graph.Generation, receiptID).Scan(&raw); e != nil {
		return nil, e
	}
	var out GraphCoverageReceipt
	e = json.Unmarshal(raw, &out)
	return &out, e
}
func (s *Server) validateGraphCoverageProofTx(ctx context.Context, q store.Query, f *frozenGraph, in GraphEvaluation) error {
	if f.Plan.Spec.Version != graph.CoverageVersion {
		if in.CoverageProof != nil {
			return importFailure("COVERAGE_PROOF_VERSION")
		}
		return nil
	}
	if in.CoverageProof == nil {
		if in.Verdict == "PASS" {
			return importFailure("COVERAGE_PROOF_REQUIRED")
		}
		return nil
	}
	ref := in.CoverageProof
	if !job.ValidKey(ref.ReceiptID) || !job.ValidHash(ref.SHA256) {
		return importFailure("COVERAGE_PROOF_INVALID")
	}
	var raw []byte
	if e := q.QueryRowContext(ctx, `SELECT body FROM iu_coverage_receipts WHERE goal_id=? AND generation=? AND receipt_id=?`, f.GoalID, f.Generation, ref.ReceiptID).Scan(&raw); e != nil {
		return importFailure("COVERAGE_PROOF_MISSING")
	}
	if graph.Hash(raw) != ref.SHA256 {
		return importFailure("COVERAGE_PROOF_HASH")
	}
	var r GraphCoverageReceipt
	if e := json.Unmarshal(raw, &r); e != nil {
		return e
	}
	p, ok := s.cfg.Jobs.GraphCoverageVerifiers[r.Request.Profile]
	c := f.Plan.Spec.Coverage
	if !ok || !p.Valid() || f.CoverageVerifier == nil || graph.Hash(job.JSON(p)) != r.ProfileHash || graph.Hash(job.JSON(f.CoverageVerifier)) != r.ProfileHash || r.Request.ExpectedValidityRevision != in.ExpectedValidityRevision || r.Request.OutputHash != in.OutputHash || r.PlanHash != f.Plan.Digest || r.CatalogHash != c.SHA256 || r.GoalSpecHash != c.GoalSpecHash {
		return importFailure("COVERAGE_PROOF_STALE")
	}
	if in.Verdict == "PASS" {
		if r.Verdict != "PASS" || len(r.Checks) != len(c.Checks) {
			return importFailure("COVERAGE_PROOF_INCOMPLETE")
		}
		seen := map[string]bool{}
		for _, ck := range r.Checks {
			if ck.Verdict != "PASS" || seen[ck.ID] {
				return importFailure("COVERAGE_PROOF_INCOMPLETE")
			}
			seen[ck.ID] = true
		}
		for _, b := range c.Checks {
			if !seen[b.ID] {
				return importFailure("COVERAGE_PROOF_INCOMPLETE")
			}
		}
	}
	for _, a := range r.Artifacts {
		var n int
		if e := q.QueryRowContext(ctx, `SELECT count(*) FROM artifacts a JOIN artifact_refs r ON r.artifact=a.id WHERE a.id=? AND a.state='ACCEPTED' AND r.ref_id=?`, a, fmt.Sprintf("coverage/%s/%d/%s", f.GoalID, f.Generation, ref.ReceiptID)).Scan(&n); e != nil {
			return e
		}
		if n == 0 {
			return importFailure("COVERAGE_EVIDENCE_UNAVAILABLE")
		}
	}
	return nil
}
func (s *Server) httpVerifyGraphCoverage(w http.ResponseWriter, r *http.Request) {
	raw, e := readJSONBody(w, r, 4096)
	if e != nil {
		httpError(w, e)
		return
	}
	var in GraphCoverageRequest
	if e = jsonutil.Decode(raw, &in); e != nil {
		httpError(w, status.Error(codes.InvalidArgument, "COVERAGE_REQUEST_INVALID"))
		return
	}
	out, e := s.VerifyGraphCoverage(r.Context(), r.PathValue("id"), in)
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
func (s *Server) httpGraphCoverage(w http.ResponseWriter, r *http.Request) {
	out, e := s.GraphCoverageReceipt(r.Context(), r.PathValue("id"), r.PathValue("receipt"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
