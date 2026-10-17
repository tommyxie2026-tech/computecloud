package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newOfflineGraphHarness(t *testing.T) *jobHarness {
	t.Helper()
	s, ctx, _, _ := offlineJobServer(t)
	s.cfg.Jobs.GraphsEnabled = true
	p, _ := rpcutil.PrincipalFrom(ctx)
	p.Identity.Scopes = append(p.Identity.Scopes, "goals:evaluate", "goals:invalidate", "goals:propose")
	h := &jobHarness{s: s, ctx: rpcutil.WithPrincipal(ctx, p), commit: strings.Repeat("a", 40)}
	t.Cleanup(func() { h.s.Close() })
	return h
}
func invalidationFixture(t *testing.T, h *jobHarness, key string) (*Job, graph.InvalidationEvent) {
	t.Helper()
	spec := graphSpec(h)
	spec.Sources = []graph.Source{{ID: "rule", Text: "discount=1000"}}
	spec.Nodes[0].Spans = []graph.Span{{Source: "rule", Start: 0, End: 13, SHA256: graph.Hash([]byte("discount=1000"))}}
	j, e := h.s.SubmitGraph(h.ctx, key, job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	return j, graph.InvalidationEvent{ID: "changed", GoalID: j.graph.GoalID, Generation: j.graph.Generation, Reason: "RULE_CHANGED", EvidenceHash: strings.Repeat("b", 64), Sources: []graph.SourceRef{{ID: "rule", SHA256: graph.Hash([]byte("discount=1000"))}}}
}
func TestGraphInvalidationSurvivesRestart(t *testing.T) {
	h := newOfflineGraphHarness(t)
	j, in := invalidationFixture(t, h, "restart-invalid")
	raw := graphCandidateOutput(t, h, j)
	var output graph.ExecutionOutput
	if e := json.Unmarshal(raw, &output); e != nil {
		t.Fatal(e)
	}
	output.Delta.Records = []graph.MemoryRecord{
		{ID: "base", Kind: "observation", Payload: json.RawMessage(`1`), Sources: in.Sources, Validation: "unchecked", Validity: "VALID"},
		{ID: "middle", Kind: "result", Payload: json.RawMessage(`2`), Dependencies: []string{"base"}, Validation: "unchecked", Validity: "VALID"},
		{ID: "end", Kind: "result", Payload: json.RawMessage(`3`), Dependencies: []string{"middle"}, Validation: "unchecked", Validity: "VALID"},
	}
	seedGraphCandidate(t, h, j, job.JSON(output))
	if e := h.s.reconcileGraphMemory(h.ctx); e != nil {
		t.Fatal(e)
	}
	var original []byte
	h.s.db.SQL.QueryRow(`SELECT output_view FROM iu_nodes WHERE goal_id=?`, j.graph.GoalID).Scan(&original)
	if e := h.s.InvalidateGraph(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	cfg := h.s.cfg
	if e := h.s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	h.s = reopened
	if e = h.s.reconcileGraphInvalidations(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.s.reconcileGraphInvalidations(h.ctx); e != nil {
		t.Fatal(e)
	}
	v, e := h.s.GraphMemory(h.ctx, j.ID, "extract")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range v.Records {
		if r.Validity != "INVALID" {
			t.Fatalf("not invalid: %+v", r)
		}
	}
	var now []byte
	h.s.db.SQL.QueryRow(`SELECT output_view FROM iu_nodes WHERE goal_id=?`, j.graph.GoalID).Scan(&now)
	if string(now) != string(original) {
		t.Fatal("immutable view changed")
	}
	st, e := h.s.GraphValidity(h.ctx, j.ID)
	if e != nil || st.Revision != 1 || len(st.Events) != 1 || st.Events[0].State != "APPLIED" {
		t.Fatalf("status %+v %v", st, e)
	}
	if e = h.s.InvalidateGraph(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	in.Reason = "SOURCE_REVISED"
	if e = h.s.InvalidateGraph(h.ctx, j.ID, in); status.Code(e) != codes.AlreadyExists {
		t.Fatalf("conflict %v", e)
	}
}
func TestInvalidationBlocksLateAdmissionAndPass(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "evaluating"}[admitted], func(t *testing.T) {
			h := newOfflineGraphHarness(t)
			j, in := invalidationFixture(t, h, "late")
			seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
			if admitted {
				if e := h.s.reconcileGraphMemory(h.ctx); e != nil {
					t.Fatal(e)
				}
				if e := h.s.advanceGraph(h.ctx, j.ID); e != nil {
					t.Fatal(e)
				}
			}
			if e := h.s.InvalidateGraph(h.ctx, j.ID, in); e != nil {
				t.Fatal(e)
			}
			if e := h.s.reconcileGraphMemory(h.ctx); e != nil {
				t.Fatal(e)
			}
			if !admitted {
				var state string
				h.s.db.SQL.QueryRow(`SELECT state FROM iu_admissions WHERE goal_id=?`, j.graph.GoalID).Scan(&state)
				if state != "REJECTED" {
					t.Fatalf("late admission %s", state)
				}
			}
			st, e := h.s.GraphStatus(h.ctx, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			ev := GraphEvaluation{ID: "bad-pass", ExpectedValidityRevision: 1, OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: in.EvidenceHash}
			if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e == nil {
				t.Fatal("stale evidence passed")
			}
			var taskState string
			h.s.db.SQL.QueryRow(`SELECT state FROM tasks WHERE job_id=?`, j.ID).Scan(&taskState)
			if taskState != "SUCCEEDED" {
				t.Fatalf("execution rewritten %s", taskState)
			}
		})
	}
	h := newOfflineGraphHarness(t)
	j, in := invalidationFixture(t, h, "not-ready")
	if e := h.s.InvalidateGraph(h.ctx, j.ID, in); e != nil {
		t.Fatal(e)
	}
	if e := h.s.advanceGraph(h.ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	var count int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, j.ID).Scan(&count)
	if count != 0 {
		t.Fatal("activated after invalidation")
	}
}
func TestInvalidationRejectsUnauthorizedOrStaleRevision(t *testing.T) {
	h := newOfflineGraphHarness(t)
	j, in := invalidationFixture(t, h, "unauthorized")
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.Identity.Scopes = []string{"jobs:read", "jobs:submit"}
	if e := h.s.InvalidateGraph(rpcutil.WithPrincipal(context.Background(), p), j.ID, in); status.Code(e) != codes.PermissionDenied {
		t.Fatalf("authorization %v", e)
	}
	in.ExpectedRevision = 5
	if e := h.s.InvalidateGraph(h.ctx, j.ID, in); status.Code(e) != codes.Aborted {
		t.Fatalf("revision %v", e)
	}
	in.ExpectedRevision = 0
	in.Generation = 2
	if e := h.s.InvalidateGraph(h.ctx, j.ID, in); e == nil {
		t.Fatal("wrong generation accepted")
	}
	var count int
	e := h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_validity_events`).Scan(&count)
	if e != nil || count != 0 {
		t.Fatalf("failed write persisted %d %v", count, e)
	}
}

func TestInvalidationRejectsRevokedManagedKeyAndRollsBack(t *testing.T) {
	h := newOfflineGraphHarness(t)
	j, in := invalidationFixture(t, h, "revoked")
	key := store.APIKey{ID: "managed-invalid", Digest: store.Hash([]byte("test-secret")), Prefix: "cc_live_test", Owner: "owner", Projects: []string{"project"}, Scopes: []string{"jobs:read", "goals:invalidate"}, CreatedAt: store.Now(), ExpiresAt: store.Now() + 60000}
	if e := h.s.db.CreateAPIKey(h.ctx, key); e != nil {
		t.Fatal(e)
	}
	p, _ := rpcutil.PrincipalFrom(h.ctx)
	p.ManagedKeyDigest = key.Digest
	cached := rpcutil.WithPrincipal(context.Background(), p)
	if e := h.s.db.RevokeAPIKey(h.ctx, "owner", key.ID, store.Now()); e != nil {
		t.Fatal(e)
	}
	if e := h.s.InvalidateGraph(cached, j.ID, in); status.Code(e) != codes.PermissionDenied {
		t.Fatalf("revoked key %v", e)
	}
	if _, e := h.s.db.SQL.Exec(`CREATE TRIGGER reject_invalidation_progress BEFORE INSERT ON iu_validity_progress BEGIN SELECT RAISE(ABORT,'injected failure'); END`); e != nil {
		t.Fatal(e)
	}
	if e := h.s.InvalidateGraph(h.ctx, j.ID, in); e == nil {
		t.Fatal("injected write succeeded")
	}
	var count int
	if e := h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_validity_events`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("partial event %d %v", count, e)
	}
	st, e := h.s.GraphValidity(h.ctx, j.ID)
	if e != nil || st.Revision != 0 {
		t.Fatalf("partial revision %+v %v", st, e)
	}
}
func TestInvalidationLinearizesWithPass(t *testing.T) {
	for _, eventFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "event-first", false: "pass-first"}[eventFirst], func(t *testing.T) {
			h := newOfflineGraphHarness(t)
			j, in := invalidationFixture(t, h, "linear")
			seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
			if e := h.s.reconcileGraphMemory(h.ctx); e != nil {
				t.Fatal(e)
			}
			if e := h.s.advanceGraph(h.ctx, j.ID); e != nil {
				t.Fatal(e)
			}
			st, e := h.s.GraphStatus(h.ctx, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			ev := GraphEvaluation{ID: "pass", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "PASS", EvidenceHash: in.EvidenceHash}
			if eventFirst {
				if e = h.s.InvalidateGraph(h.ctx, j.ID, in); e != nil {
					t.Fatal(e)
				}
				ev.ExpectedValidityRevision = 1
				if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); status.Code(e) != codes.FailedPrecondition {
					t.Fatalf("pass after event %v", e)
				}
			} else {
				if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
					t.Fatal(e)
				}
				if e = h.s.InvalidateGraph(h.ctx, j.ID, in); status.Code(e) != codes.FailedPrecondition {
					t.Fatalf("terminal mutation %v", e)
				}
				in.AuditOnly = true
				if e = h.s.InvalidateGraph(h.ctx, j.ID, in); e != nil {
					t.Fatal(e)
				}
				st, e = h.s.GraphStatus(h.ctx, j.ID)
				if e != nil || st.GoalState != "SUCCEEDED" {
					t.Fatalf("audit reopened Goal %+v %v", st, e)
				}
				validity, e := h.s.GraphValidity(h.ctx, j.ID)
				if e != nil || validity.Events[0].State != "AUDIT_ONLY" {
					t.Fatalf("audit status %+v %v", validity, e)
				}
			}
		})
	}
}

func TestInvalidationProjectionBatchesLeaveDurableBacklog(t *testing.T) {
	h := newOfflineGraphHarness(t)
	j, in := invalidationFixture(t, h, "batch")
	for i := 0; i < 5; i++ {
		in.ID = fmt.Sprintf("event-%d", i)
		in.ExpectedRevision = int64(i)
		if e := h.s.InvalidateGraph(h.ctx, j.ID, in); e != nil {
			t.Fatal(e)
		}
	}
	if e := h.s.reconcileGraphInvalidations(h.ctx); e != nil {
		t.Fatal(e)
	}
	var pending int
	if e := h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_validity_progress WHERE state='RECORDED'`).Scan(&pending); e != nil {
		t.Fatal(e)
	}
	if pending < 1 || pending > 4 {
		t.Fatalf("expected bounded progress and durable backlog, pending=%d", pending)
	}
	if e := h.s.reconcileGraphInvalidations(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e := h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_validity_progress WHERE state='RECORDED'`).Scan(&pending); e != nil || pending != 0 {
		t.Fatalf("backlog not recovered %d %v", pending, e)
	}
}

func TestInvalidationRejectsReplanUsingOldEvaluation(t *testing.T) {
	h := newOfflineGraphHarness(t)
	spec := graphSpec(h)
	spec.Limits.MaxReplans = 1
	spec.Sources = []graph.Source{{ID: "rule", Text: "old"}}
	j, e := h.s.SubmitGraph(h.ctx, "old-replan", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	seedGraphCandidate(t, h, j, graphCandidateOutput(t, h, j))
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
	ev := GraphEvaluation{ID: "fail", OutputHash: st.OutputHash, Verifier: "external", VerifierVersion: "1", Verdict: "FAIL", EvidenceHash: graph.Hash([]byte("failure"))}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
	st, e = h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	event := graph.InvalidationEvent{ID: "new-evidence", GoalID: j.graph.GoalID, Generation: 1, Reason: "SOURCE_REVISED", EvidenceHash: graph.Hash([]byte("new")), Sources: []graph.SourceRef{{ID: "rule", SHA256: graph.Hash([]byte("old"))}}}
	if e = h.s.InvalidateGraph(h.ctx, j.ID, event); e != nil {
		t.Fatal(e)
	}
	spec.Nodes[0].Instructions = "new strategy"
	compiled, e := graph.Compile(spec)
	if e != nil {
		t.Fatal(e)
	}
	proposal := replanProposal(st.GoalID, st.EvaluationID, h.spec("single"))
	proposal.ProposedPlan.JobSpecHash = compiled.Digest
	proposal.Evidence = goal.ReplanEvidence{FailureClass: goal.FailureTest, Evidence: []goal.Evidence{{Type: "iu_graph_evaluation", Fact: graphEvaluationFact(ev)}}}
	if _, _, e = h.s.PublishGoalReplan(h.ctx, j.ID, proposal, "", job.JSON(spec)); e == nil || !strings.Contains(e.Error(), "GRAPH_VALIDITY_STALE") {
		t.Fatalf("stale replan %v", e)
	}
	var count int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_graphs WHERE goal_id=?`, st.GoalID).Scan(&count)
	if count != 1 {
		t.Fatal("new generation published from stale evidence")
	}
}
