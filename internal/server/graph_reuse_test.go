package server

import (
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"testing"
)

func reuseSource(t *testing.T) (*jobHarness, *Job, graph.Spec, GraphEvaluation) {
	t.Helper()
	h := newOfflineGraphHarness(t)
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.Identity.Scopes = append(p.Identity.Scopes, "goals:import")
	h.ctx = rpcutil.WithPrincipal(h.ctx, p)
	spec := graphSpec(h)
	spec.Version = graph.ReuseVersion
	spec.Limits.MaxReplans = 1
	spec.Nodes[0].ReplayProfile = "fixture"
	ex := spec.Operators[0].Execution
 ex.ReplaySafe=true
	h.s.cfg.Jobs.GraphReplayBindings = map[string]graph.ReplayBinding{"fixture": {Version: "1", RuntimeProfile: ex.RuntimeProfile, RuntimeVersion: "fixture-1", Model: ex.Model, EnvironmentHash: graph.Hash([]byte("fixture env")), RuntimeConfigHash: graph.Hash([]byte("fixture runtime")), PolicyHash: graph.Hash([]byte("fixture policy")), ReadOnly: true}}
	j, e := h.s.SubmitGraph(h.ctx, "reuse-source", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	var out graph.ExecutionOutput
	if e = json.Unmarshal(graphCandidateOutput(t, h, j), &out); e != nil {
		t.Fatal(e)
	}
	out.Delta.Records = []graph.MemoryRecord{{ID: "raw", Kind: "observation", Payload: json.RawMessage(`12000`), Validity: "VALID", Validation: "unchecked"}}
	seedGraphCandidate(t, h, j, job.JSON(out))
	if e = h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	ev := GraphEvaluation{ID: "fail", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: st.OutputHash}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
	spec.Reuse = []graph.ReuseBinding{{GrantID: "grant_a", NodeID: "extract"}}
	return h, j, spec, ev
}
func TestReuseSurvivesRestartWithoutNewAttempt(t *testing.T) {
	h, j, spec, ev := reuseSource(t)
	in := makeImportGrant(t, h, j, spec)
	in.Purpose = "node_result"
	if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	next, e := publishImport(t, h, j, spec, ev)
	if e != nil {
		t.Fatal(e)
	}
	cfg := h.s.cfg
	if e = h.s.Close(); e != nil {
		t.Fatal(e)
	}
	h.s, e = New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = h.s.advanceGraph(h.ctx, next.ID); e != nil {
			t.Fatal(e)
		}
	}
	var tasks, attempts int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, next.ID).Scan(&tasks)
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=?`, next.ID).Scan(&attempts)
	if tasks != 0 || attempts != 0 {
		t.Fatalf("fake execution tasks=%d attempts=%d", tasks, attempts)
	}
	st, e := h.s.GraphStatus(h.ctx, next.ID)
	if e != nil || st.State != "SUCCEEDED" || st.GoalState != "EVALUATING" || st.Nodes[0].ExecutionKind != "REUSED" {
		t.Fatalf("status %+v %v", st, e)
	}
}
func TestReusePinsOriginArtifacts(t *testing.T) {
	h, j, spec, ev := reuseSource(t)
	in := makeImportGrant(t, h, j, spec)
	in.Purpose = "node_result"
	if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	next, e := publishImport(t, h, j, spec, ev)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, next.ID); e != nil {
		t.Fatal(e)
	}
	var artifact string
	if e = h.s.db.SQL.QueryRow(`SELECT artifact FROM artifact_refs WHERE ref_id LIKE 'reuse/%' LIMIT 1`).Scan(&artifact); e != nil {
		t.Fatal(e)
	}
	if _, e = h.s.db.SQL.Exec(`UPDATE artifacts SET state='DELETING' WHERE id=?`, artifact); e == nil {
		t.Fatal("GC deleted reused origin")
	}
}
func TestIncrementalEqualsFullRecompute(t *testing.T) {
	h, j, spec, ev := reuseSource(t)
	in := makeImportGrant(t, h, j, spec)
	in.Purpose = "node_result"
	if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	next, e := publishImport(t, h, j, spec, ev)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.s.advanceGraph(h.ctx, next.ID); e != nil {
		t.Fatal(e)
	}
	var oldHash, newHash string
	h.s.db.SQL.QueryRow(`SELECT output_hash FROM iu_graphs WHERE job_id=?`, j.ID).Scan(&oldHash)
	h.s.db.SQL.QueryRow(`SELECT output_hash FROM iu_graphs WHERE job_id=?`, next.ID).Scan(&newHash)
	if oldHash != newHash {
		t.Fatal("outputs differ")
	}
}
func TestReuseChangedInputFallsBackToNewTask(t *testing.T) {
	for _, change := range []string{"instructions", "model", "cancel", "missing-binding", "unknown"} {
		t.Run(change, func(t *testing.T) {
			h, j, spec, ev := reuseSource(t)
			if change == "instructions" {
				spec.Nodes[0].Instructions += " changed"
			}
			if change == "model" {
				spec.Operators[0].Execution.Model = "another"
			}
			if change == "unknown" {
				if _, e := h.s.db.SQL.Exec(`UPDATE iu_nodes SET output_json='{"result":"UNKNOWN"}' WHERE goal_id=?`, j.graph.GoalID); e != nil {
					t.Fatal(e)
				}
			}
			if change == "missing-binding" {
				spec.Nodes[0].ReplayProfile = "absent"
			}
			in := makeImportGrant(t, h, j, spec)
			in.Purpose = "node_result"
			if _, e := h.s.IssueGraphImport(h.ctx, j.ID, in); e != nil {
				t.Fatal(e)
			}
			next, e := publishImport(t, h, j, spec, ev)
			if change == "model" || change == "missing-binding" {
				if e == nil {
					t.Fatal("unregistered binding accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if change == "cancel" {
				if _, e = h.s.CancelJob(h.ctx, next.ID, CancelJobRequest{ControlID: "stop", Reason: "test"}); e != nil {
					t.Fatal(e)
				}
			}
			if e = h.s.advanceGraph(h.ctx, next.ID); e != nil {
				t.Fatal(e)
			}
			var tasks int
			h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, next.ID).Scan(&tasks)
			want := 1
			if change == "cancel" {
				want = 0
			}
			if tasks != want {
				t.Fatalf("tasks %d want %d", tasks, want)
			}
		})
	}
}
