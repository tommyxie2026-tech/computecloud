package server

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func seedGraphCandidate(t *testing.T, h *jobHarness, j *Job, raw []byte) string {
	t.Helper()
	if e := h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	var tid string
	if e := h.s.db.SQL.QueryRow(`SELECT task_id FROM iu_nodes WHERE goal_id=? AND generation=? AND node_id='extract'`, j.graph.GoalID, j.graph.Generation).Scan(&tid); e != nil {
		t.Fatal(e)
	}
	aid := store.ID()
	artifact := store.ID()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, item := range []struct {
		name string
		body []byte
	}{{"report.json", []byte(`{}`)}, {"changes.patch", nil}, {"stderr.log", nil}, {"iu-output.json", raw}} {
		if e := tw.WriteHeader(&tar.Header{Name: item.name, Size: int64(len(item.body)), Mode: 0600}); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write(item.body); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(h.s.cfg.DataDir, "artifacts")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, artifact), b.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	e := h.s.db.Tx(h.ctx, func(q store.Query) error {
		if _, e := q.ExecContext(h.ctx, `INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released) VALUES(?,?,'fixture','epoch',1,'token',?,1)`, aid, tid, store.Now()+60000); e != nil {
			return e
		}
		if _, e := q.ExecContext(h.ctx, `UPDATE tasks SET state='SUCCEEDED',attempt=?,current_generation=1 WHERE id=?`, aid, tid); e != nil {
			return e
		}
		if _, e := q.ExecContext(h.ctx, `INSERT INTO artifacts(id,task,attempt,kind,hash,size,path,generation,state,created,updated) VALUES(?,?,?,'result-bundle',?,?,?,1,'STAGED',?,?)`, artifact, tid, aid, graph.Hash(b.Bytes()), b.Len(), artifact, store.Now(), store.Now()); e != nil {
			return e
		}
		if e := acceptArtifact(h.ctx, q, artifact, aid, tid, 1); e != nil {
			return e
		}
		return enqueueGraphCandidates(h.ctx, q, tid, []string{artifact})
	})
	if e != nil {
		t.Fatal(e)
	}
	return artifact
}

func graphCandidateOutput(t *testing.T, h *jobHarness, j *Job) []byte {
	t.Helper()
	if e := h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	var ctx, view []byte
	if e := h.s.db.SQL.QueryRow(`SELECT context_json,input_view FROM iu_nodes WHERE goal_id=? AND generation=? AND node_id='extract'`, j.graph.GoalID, j.graph.Generation).Scan(&ctx, &view); e != nil {
		t.Fatal(e)
	}
	var m graph.ContextManifest
	var v graph.MemoryView
	json.Unmarshal(ctx, &m)
	json.Unmarshal(view, &v)
	return job.JSON(graph.ExecutionOutput{Version: "iu-execution-output-v2", GoalID: j.graph.GoalID, Generation: j.graph.Generation, NodeID: "extract", ContextHash: m.SHA256, Values: map[string]json.RawMessage{"result": json.RawMessage(`11000`)}, Delta: graph.Delta{Version: "iu-memory-delta-v1", ContextHash: m.SHA256, ReadViewHash: v.SHA256, Records: []graph.MemoryRecord{}}})
}

func TestGraphMemoryAdmissionRecoversAndDoesNotReplayTask(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	j, e := h.s.SubmitGraph(h.ctx, "memory", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
	for i := 0; i < 2; i++ {
		if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
			t.Fatal(e)
		}
		if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
			t.Fatal(e)
		}
	}
	var state string
	var n int
	if e = h.s.db.SQL.QueryRow(`SELECT state FROM iu_admissions WHERE goal_id=?`, j.graph.GoalID).Scan(&state); e != nil || state != "ADMITTED" {
		t.Fatalf("admission=%s %v", state, e)
	}
	if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, j.ID).Scan(&n); e != nil || n != 1 {
		t.Fatalf("duplicate task %d %v", n, e)
	}
	current, e := h.s.GetJob(h.ctx, j.ID)
	if e != nil || current.State != "SUCCEEDED" {
		t.Fatalf("job %+v %v", current, e)
	}
	projection, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil || projection.GoalState != "EVALUATING" {
		t.Fatalf("premature Goal terminal %+v %v", projection, e)
	}
}

func TestGraphMemoryPermanentRejectionTerminatesGraph(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.GraphsEnabled = true })
	j, e := h.s.SubmitGraph(h.ctx, "bad-memory", job.JSON(graphSpec(h)))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, []byte(`{"malformed":true}`))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = h.s.db.SQL.QueryRow(`SELECT state FROM iu_admissions WHERE goal_id=?`, j.graph.GoalID).Scan(&state); e != nil || state != "REJECTED" {
		t.Fatalf("%s %v", state, e)
	}
	current, e := h.s.GetJob(h.ctx, j.ID)
	if e != nil || current.State != "FAILED" {
		t.Fatalf("hung graph %+v %v", current, e)
	}
	var taskState string
	if e = h.s.db.SQL.QueryRow(`SELECT state FROM tasks WHERE job_id=?`, j.ID).Scan(&taskState); e != nil || taskState != "SUCCEEDED" {
		t.Fatalf("rewrote execution fact %s %v", taskState, e)
	}
}
