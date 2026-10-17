package server

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"golang.org/x/sys/unix"
)

// Called in the same transaction as Artifact acceptance and completion. The
// immutable artifact hash is the candidate identity; no model field authorizes it.
func enqueueGraphCandidates(ctx context.Context, q store.Query, taskID string, artifacts []string) error {
	var goalID, nodeID string
	var generation int64
	e := q.QueryRowContext(ctx, `SELECT goal_id,generation,node_id FROM iu_nodes WHERE task_id=?`, taskID).Scan(&goalID, &generation, &nodeID)
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		return e
	}
	for _, id := range artifacts {
		var hash string
		if e = q.QueryRowContext(ctx, `SELECT hash FROM artifacts WHERE id=? AND task=? AND state='ACCEPTED'`, id, taskID).Scan(&hash); e != nil {
			return e
		}
		var previous string
		e = q.QueryRowContext(ctx, `SELECT candidate_hash FROM iu_admissions WHERE goal_id=? AND generation=? AND node_id=? AND artifact_id=? AND output_port='memory'`, goalID, generation, nodeID, id).Scan(&previous)
		if e == nil {
			if previous != hash {
				return fmt.Errorf("IDEMPOTENCY_CONFLICT")
			}
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO iu_admissions(goal_id,generation,node_id,artifact_id,output_port,candidate_hash,state,receipt_id,created) VALUES(?,?,?,?,'memory',?,'PENDING',?,?)`, goalID, generation, nodeID, id, hash, "receipt_"+store.ID(), store.Now()); e != nil {
			return e
		}
		if e = pinArtifactRef(ctx, q, id, "iu_pending", goalID+fmt.Sprintf("/%d/", generation)+nodeID); e != nil {
			return e
		}
	}
	return nil
}

type graphCandidate struct {
	GoalID, NodeID, ArtifactID, Hash, Path, JobID string
	Generation, Size, Deadline                    int64
	Context, View                                 []byte
	Retry                                         int
}

func readGraphBundle(path string, size int64, hash string) ([]byte, error) {
	if size < 0 || size > 32<<20 {
		return nil, fmt.Errorf("IU_BUNDLE_LIMIT")
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if e != nil {
		return nil, e
	}
	if int64(len(raw)) != size || graph.Hash(raw) != hash {
		return nil, fmt.Errorf("IU_ARTIFACT_HASH_MISMATCH")
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	seen := map[string]bool{}
	var output []byte
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if seen[header.Name] || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size < 0 {
			return nil, fmt.Errorf("IU_BUNDLE_MEMBER_INVALID")
		}
		seen[header.Name] = true
		limit := int64(1 << 20)
		switch header.Name {
		case "report.json", "stderr.log":
		case "changes.patch":
			limit = 0
		case "iu-output.json":
			limit = 64 << 10
		default:
			return nil, fmt.Errorf("IU_BUNDLE_MEMBER_UNDECLARED")
		}
		if header.Size > limit {
			return nil, fmt.Errorf("IU_BUNDLE_MEMBER_LIMIT")
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			return nil, e
		}
		if header.Name == "iu-output.json" {
			output = b
		}
	}
	if len(output) == 0 || !seen["report.json"] || !seen["changes.patch"] || !seen["stderr.log"] {
		return nil, fmt.Errorf("IU_BUNDLE_MEMBER_MISSING")
	}
	return output, nil
}

func (s *Server) reconcileGraphMemory(ctx context.Context) error {
	rows, e := s.db.SQL.QueryContext(ctx, `SELECT m.goal_id,m.generation,m.node_id,m.artifact_id,m.candidate_hash,a.path,a.size,g.job_id,j.deadline,n.context_json,n.input_view,m.retry_count FROM iu_admissions m JOIN artifacts a ON a.id=m.artifact_id JOIN iu_graphs g ON g.goal_id=m.goal_id AND g.generation=m.generation JOIN jobs j ON j.id=g.job_id JOIN iu_nodes n ON n.goal_id=m.goal_id AND n.generation=m.generation AND n.node_id=m.node_id WHERE m.state='PENDING' AND m.next_retry_at<=? ORDER BY m.created,m.receipt_id LIMIT 16`, store.Now())
	if e != nil {
		return e
	}
	var candidates []graphCandidate
	for rows.Next() {
		var c graphCandidate
		if e = rows.Scan(&c.GoalID, &c.Generation, &c.NodeID, &c.ArtifactID, &c.Hash, &c.Path, &c.Size, &c.JobID, &c.Deadline, &c.Context, &c.View, &c.Retry); e != nil {
			rows.Close()
			return e
		}
		candidates = append(candidates, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, c := range candidates {
		if e = s.admitGraphCandidate(ctx, c); e != nil {
			return e
		}
	}
	return nil
}

func (s *Server) admitGraphCandidate(ctx context.Context, c graphCandidate) error {
	var out graph.ExecutionOutput
	var view, next graph.MemoryView
	var manifest graph.ContextManifest
	raw, parseErr := readGraphBundle(filepath.Join(s.cfg.DataDir, "artifacts", c.Path), c.Size, c.Hash)
	transient := parseErr != nil && os.IsNotExist(parseErr)
	if parseErr == nil {
		parseErr = jsonutil.Decode(raw, &out)
	}
	if parseErr == nil {
		parseErr = json.Unmarshal(c.View, &view)
	}
	if parseErr == nil {
		parseErr = json.Unmarshal(c.Context, &manifest)
	}
	if parseErr == nil && (out.Version != "iu-execution-output-v2" || out.GoalID != c.GoalID || out.Generation != c.Generation || out.NodeID != c.NodeID || out.ContextHash != manifest.SHA256 || out.Delta.ContextHash != manifest.SHA256) {
		parseErr = fmt.Errorf("IU_OUTPUT_IDENTITY_MISMATCH")
	}
	if parseErr == nil {
		next, parseErr = graph.ApplyDelta(view, out.Delta)
	}
	return s.db.Tx(ctx, func(q store.Query) error {
		var currentState string
		if e := q.QueryRowContext(ctx, `SELECT state FROM iu_admissions WHERE goal_id=? AND generation=? AND node_id=? AND artifact_id=? AND output_port='memory'`, c.GoalID, c.Generation, c.NodeID, c.ArtifactID).Scan(&currentState); e != nil {
			return e
		}
		if currentState != "PENDING" {
			return nil
		}
		var activeGeneration int64
		var goalState, jobStop string
		if e := q.QueryRowContext(ctx, `SELECT g.active_graph_generation,g.state,j.stop_reason FROM goals g JOIN jobs j ON j.id=? WHERE g.id=?`, c.JobID, c.GoalID).Scan(&activeGeneration, &goalState, &jobStop); e != nil {
			return e
		}
		reason := ""
		if activeGeneration != c.Generation {
			reason = "STALE_GENERATION"
		} else if goalState == "CANCELED" || jobStop != "" {
			reason = "CANCELED"
		} else if c.Deadline <= store.Now() {
			reason = "MEMORY_ADMISSION_DEADLINE_EXCEEDED"
		}
		if reason == "" && transient {
			delays := []int64{1000, 2000, 4000, 8000, 16000, 30000}
			delay := delays[min(c.Retry, len(delays)-1)]
			_, e := q.ExecContext(ctx, `UPDATE iu_admissions SET retry_count=retry_count+1,next_retry_at=?,error_code='STORAGE_UNAVAILABLE' WHERE goal_id=? AND generation=? AND node_id=? AND artifact_id=? AND state='PENDING'`, store.Now()+delay, c.GoalID, c.Generation, c.NodeID, c.ArtifactID)
			return e
		}
		var valid int
		if reason == "" {
			if e := q.QueryRowContext(ctx, `SELECT count(*) FROM iu_nodes n JOIN tasks t ON t.id=n.task_id JOIN attempts x ON x.id=t.attempt JOIN artifacts a ON a.task=t.id AND a.attempt=x.id JOIN artifact_refs r ON r.artifact=a.id AND r.ref_type='task_result' AND r.ref_id=t.id WHERE n.goal_id=? AND n.generation=? AND n.node_id=? AND n.context_json=? AND n.input_view=? AND t.job_id=? AND t.state='SUCCEEDED' AND x.released=1 AND t.current_generation=x.generation AND a.generation=x.generation AND a.id=? AND a.hash=? AND a.state='ACCEPTED'`, c.GoalID, c.Generation, c.NodeID, c.Context, c.View, c.JobID, c.ArtifactID, c.Hash).Scan(&valid); e != nil {
				return e
			}
			if valid != 1 {
				reason = "IU_PROVENANCE_REJECTED"
			}
		}
		if reason == "" && parseErr != nil {
			reason = "MEMORY_ADMISSION_REJECTED"
		}
		j, e := readJob(ctx, q, c.JobID)
		if e != nil {
			return e
		}
		if reason == "" {
			n, ok := graphNode(j.graph, c.NodeID)
			if !ok {
				return fmt.Errorf("GRAPH_NODE_MISSING")
			}
			op := graphOperator(j.graph, n)
			if len(out.Values) != len(op.Outputs) {
				reason = "IU_OUTPUT_PORT_REJECTED"
			}
			for _, port := range op.Outputs {
				if !json.Valid(out.Values[port.Name]) {
					reason = "IU_OUTPUT_PORT_REJECTED"
				}
			}
			if graph.ValidateValues(out.Values, op.Outputs, j.graph.Plan.Spec.Schemas) != nil {
				reason = "IU_OUTPUT_SCHEMA_REJECTED"
			}
			allowedSources := map[string]string{}
			for _, span := range manifest.Spans {
				allowedSources[span.Source] = span.SHA256
			}
			for _, record := range view.Records {
				for _, src := range record.Sources {
					allowedSources[src.ID] = src.SHA256
				}
			}
			for _, record := range out.Delta.Records {
				for _, src := range record.Sources {
					if allowedSources[src.ID] != src.SHA256 {
						reason = "IU_SOURCE_REJECTED"
					}
				}
			}
		}
		if reason != "" {
			if _, e = q.ExecContext(ctx, `UPDATE iu_admissions SET state='REJECTED',error_code=? WHERE goal_id=? AND generation=? AND node_id=? AND artifact_id=? AND state='PENDING'`, reason, c.GoalID, c.Generation, c.NodeID, c.ArtifactID); e != nil {
				return e
			}
			if reason == "STALE_GENERATION" || reason == "CANCELED" {
				return nil
			}
			if _, e = q.ExecContext(ctx, `UPDATE iu_nodes SET state='FAILED',error_code=? WHERE goal_id=? AND generation=? AND node_id=?`, reason, c.GoalID, c.Generation, c.NodeID); e != nil {
				return e
			}
			return jobState(ctx, q, j, "STOPPING", reason, reason)
		}
		if e = pinArtifactRef(ctx, q, c.ArtifactID, "iu_memory", c.GoalID+fmt.Sprintf("/%d/", c.Generation)+c.NodeID); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `UPDATE iu_admissions SET state='ADMITTED',view_json=?,error_code='' WHERE goal_id=? AND generation=? AND node_id=? AND artifact_id=? AND state='PENDING'`, job.JSON(next), c.GoalID, c.Generation, c.NodeID, c.ArtifactID); e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `UPDATE iu_nodes SET output_json=?,output_view=? WHERE goal_id=? AND generation=? AND node_id=?`, job.JSON(out.Values), job.JSON(next), c.GoalID, c.Generation, c.NodeID)
		return e
	})
}
