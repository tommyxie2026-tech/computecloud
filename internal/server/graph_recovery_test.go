package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/goal"
	"github.com/tommyxie2026-tech/computecloud/internal/governance"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func recoverySource(t *testing.T) (*jobHarness, *Job, graph.RecoveryProposal) {
	h, j := coverageJob(t, 12000)
	return h, j, makeRecoveryProposal(t, h, j)
}
func makeRecoveryProposal(t *testing.T, h *jobHarness, j *Job) graph.RecoveryProposal {
	in := coverageRequest(t, h, j)
	r, e := h.s.VerifyGraphCoverage(h.ctx, j.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	proof := &GraphCoverageProofRef{ReceiptID: in.ID, SHA256: graph.Hash(job.JSON(r))}
	ev := GraphEvaluation{ID: "fail", OutputHash: in.OutputHash, Verifier: j.graph.Plan.Spec.Evaluator.Profile, VerifierVersion: j.graph.Plan.Spec.Evaluator.Version, Verdict: "FAIL", EvidenceHash: r.EvidenceHash, CoverageProof: proof}
	if e = h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
		t.Fatal(e)
	}
	st, e := h.s.GraphStatus(h.ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	src := j.graph.Plan.Spec.Sources[0]
	read, e := h.s.ReadGraphSource(h.ctx, j.ID, GraphSourceReadRequest{ID: "repair_read", NodeID: "extract", PlanHash: j.graph.Plan.Digest, Span: graph.Span{Source: src.ID, End: len(src.Text), SHA256: src.SHA256}})
	if e != nil {
		t.Fatal(e)
	}
	var next graph.Spec
	json.Unmarshal(job.JSON(j.graph.Plan.Spec), &next)
	next.Nodes[0].Instructions += "\nRecheck the original order identities and global rule."
	p := graph.RecoveryProposal{Version: "iu-recovery-proposal-v1", ID: fmt.Sprintf("repair-%d", st.Generation), GoalID: st.GoalID, PriorGeneration: st.Generation, PriorPlanHash: st.Plan.Digest, EvaluationID: st.EvaluationID, EvaluationHash: graph.Hash(st.Evaluation), CoverageReceiptID: proof.ReceiptID, CoverageReceiptHash: proof.SHA256, ReadReceiptIDs: []string{read.ReceiptID}, ProposedSpec: next}
	p.SHA256 = p.Digest()
	return p
}
func TestRecoveryPublicationAtomicAndIdempotent(t *testing.T) {
	h, j, p := recoverySource(t)
	next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
	if e != nil || !d.Allowed || next == nil {
		t.Fatal(d, e)
	}
	if next.graph.Generation != 2 {
		t.Fatal("generation")
	}
	again, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
	if e != nil || !d.Existing || again.ID != next.ID {
		t.Fatal("replay", d, e)
	}
	var n int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_recovery_publications`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	finishCoverageJob(t, h, next, 11000)
	in := coverageRequest(t, h, next)
	r, e := h.s.VerifyGraphCoverage(h.ctx, next.ID, in)
	if e != nil || r.Verdict != "PASS" {
		t.Fatal(r, e)
	}
}
func TestRecoveryRequiresTrustedFailAndMatchingReads(t *testing.T) {
	h, j, p := recoverySource(t)
	p.ReadReceiptIDs = []string{"invented"}
	p.SHA256 = p.Digest()
	if _, _, e := h.s.RecoverGraph(h.ctx, j.ID, p, ""); e == nil {
		t.Fatal("forged reads accepted")
	}
	_, _, e := h.s.PublishGoalReplan(h.ctx, j.ID, goal.ReplanRequest{}, "", job.JSON(p.ProposedSpec))
	if e == nil {
		t.Fatal("ordinary path bypass")
	}
}
func TestRecoveryConcurrentProposalAndLostResponse(t *testing.T) {
	h, j, p := recoverySource(t)
	var wg sync.WaitGroup
	wg.Add(8)
	results := make(chan *Job, 8)
	errors := make(chan error, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			defer wg.Done()
			<-start
			next, _, err := h.s.RecoverGraph(h.ctx, j.ID, p, "")
			results <- next
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("identical concurrent recovery: %v", err)
		}
	}
	var published string
	for next := range results {
		if next == nil {
			t.Error("missing replay Job")
			continue
		}
		if published == "" {
			published = next.ID
		}
		if next.ID != published {
			t.Errorf("different replay Jobs: %s / %s", published, next.ID)
		}
	}
	var count int
	if e := h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_recovery_publications`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	cfg := h.s.cfg
	if e := h.s.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	h.s, e = New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
	if e != nil || !d.Existing || next == nil {
		t.Fatal(d, e)
	}
}
func TestRecoveryBudgetsApprovalAndDrain(t *testing.T) {
	t.Run("transaction_rollback", func(t *testing.T) {
		h, j, p := recoverySource(t)
		_, e := h.s.db.SQL.Exec(`CREATE TRIGGER fail_recovery_publish BEFORE INSERT ON iu_recovery_publications BEGIN SELECT RAISE(ABORT,'injected publication fault'); END;`)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = h.s.RecoverGraph(h.ctx, j.ID, p, ""); e == nil {
			t.Fatal("injected failure ignored")
		}
		var gen int
		if e = h.s.db.SQL.QueryRow(`SELECT active_graph_generation FROM goals WHERE id=?`, p.GoalID).Scan(&gen); e != nil || gen != 1 {
			t.Fatal(gen, e)
		}
		h.s.db.SQL.Exec(`DROP TRIGGER fail_recovery_publish`)
		next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
		if e != nil || !d.Allowed || next == nil {
			t.Fatal(d, e)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		h, j, p := recoverySource(t)
		st, _ := h.s.GraphStatus(h.ctx, j.ID)
		ev := GraphEvaluation{ID: "unknown", ExpectedVersion: st.EvaluationVersion, Supersedes: st.EvaluationID, OutputHash: st.OutputHash, Verifier: j.graph.Plan.Spec.Evaluator.Profile, VerifierVersion: j.graph.Plan.Spec.Evaluator.Version, Verdict: "UNKNOWN", EvidenceHash: st.OutputHash}
		if e := h.s.EvaluateGraph(h.ctx, j.ID, ev); e != nil {
			t.Fatal(e)
		}
		if _, _, e := h.s.RecoverGraph(h.ctx, j.ID, p, ""); e == nil {
			t.Fatal("unknown published")
		}
	})
	t.Run("deadline", func(t *testing.T) {
		h, j, p := recoverySource(t)
		h.s.db.SQL.Exec(`UPDATE goals SET deadline=1 WHERE id=?`, p.GoalID)
		next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
		if e == nil && d.Allowed || next != nil {
			t.Fatal("expired published")
		}
	})
	t.Run("round_limit", func(t *testing.T) {
		h, j, p := recoverySource(t)
		for round := range 2 {
			next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
			if e != nil || !d.Allowed {
				t.Fatal(d, e)
			}
			j = next
			finishCoverageJob(t, h, j, 13000+round)
			p = makeRecoveryProposal(t, h, j)
		}
		next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
		if e == nil && d.Allowed || next != nil {
			t.Fatal("third recovery")
		}
	})
}
func TestRecoveryApprovalAndActiveAttempt(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		h, j, p := recoverySource(t)
		h.s.db.SQL.Exec(`UPDATE goals SET max_replans=0 WHERE id=?`, p.GoalID)
		next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, "")
		if e != nil || !d.NeedsApproval || next != nil {
			t.Fatal(d, e)
		}
		var n, version int64
		h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_recovery_publications`).Scan(&n)
		if n != 0 {
			t.Fatal("consumed denied publication")
		}
		h.s.db.SQL.Exec(`UPDATE goals SET max_replans=2 WHERE id=?`, p.GoalID)
		h.s.db.SQL.QueryRow(`SELECT version FROM goals WHERE id=?`, p.GoalID).Scan(&version)
		actor := governance.Actor{ID: "human", Kind: "human", Owner: "owner", Projects: []string{"project"}, Scopes: []string{"goals:approve"}}
		in := governance.Request{OperationID: "approve-recovery", ExpectedVersion: version, PlanRevision: 1, GraphGeneration: 1, Action: "APPROVE_NEXT_REPLAN", Reason: "reviewed pending evidence correction"}
		e = h.s.db.Tx(h.ctx, func(q store.Query) error { _, e := governance.DecideTx(h.ctx, q, actor, p.GoalID, in); return e })
		if e != nil {
			t.Fatal(e)
		}
		next, d, e = h.s.RecoverGraph(h.ctx, j.ID, p, in.OperationID)
		if e != nil || !d.Allowed || next == nil {
			t.Fatal(d, e)
		}
	})
	t.Run("drain", func(t *testing.T) {
		h, j, p := recoverySource(t)
		if _, e := h.s.db.SQL.Exec(`UPDATE attempts SET released=0 WHERE task IN (SELECT id FROM tasks WHERE job_id=?)`, j.ID); e != nil {
			t.Fatal(e)
		}
		if next, d, e := h.s.RecoverGraph(h.ctx, j.ID, p, ""); e == nil && d.Allowed || next != nil {
			t.Fatal("in-flight attempt ignored")
		}
	})
}

func TestRecoveryRechecksAuthorityAndGenerationAfterRead(t *testing.T) {
	t.Run("revoked_key", func(t *testing.T) {
		h, j, p := recoverySource(t) // A durable source read has already succeeded.
		principal, _ := rpcutil.PrincipalFrom(h.ctx)
		key := store.APIKey{ID: "recovery-key", Digest: store.Hash([]byte("recovery-test-secret")), Prefix: "cc_live_test", Owner: principal.Identity.Owner, Projects: []string{"project"}, Scopes: []string{"jobs:read", "jobs:submit", "goals:propose"}, CreatedAt: store.Now(), ExpiresAt: store.Now() + 60000}
		if err := h.s.db.CreateAPIKey(h.ctx, key); err != nil {
			t.Fatal(err)
		}
		principal.ManagedKeyDigest = key.Digest
		cached := rpcutil.WithPrincipal(context.Background(), principal)
		if err := h.s.db.RevokeAPIKey(h.ctx, key.Owner, key.ID, store.Now()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := h.s.RecoverGraph(cached, j.ID, p, ""); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("revoked read authority reused: %v", err)
		}
		var count int
		if err := h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_recovery_publications`).Scan(&count); err != nil || count != 0 {
			t.Fatal(count, err)
		}
	})
	t.Run("generation_advanced", func(t *testing.T) {
		h, j, p := recoverySource(t)
		stale := p
		stale.ID = "delayed-reader"
		stale.SHA256 = stale.Digest()
		if next, d, err := h.s.RecoverGraph(h.ctx, j.ID, p, ""); err != nil || next == nil || !d.Allowed {
			t.Fatal(d, err)
		}
		if next, _, err := h.s.RecoverGraph(h.ctx, j.ID, stale, ""); err == nil || next != nil {
			t.Fatal("stale read published a new recovery", err)
		}
		var count int
		if err := h.s.db.SQL.QueryRow(`SELECT count(*) FROM iu_recovery_publications`).Scan(&count); err != nil || count != 1 {
			t.Fatal(count, err)
		}
	})
}
func TestRecoveryOrdinaryHTTPRejectsV5Bypass(t *testing.T) {
	h, j, p := recoverySource(t)
	principal, _ := rpcutil.PrincipalFrom(h.ctx)
	principal.Identity.Scopes = append(principal.Identity.Scopes, "jobs:control")
	ctx := rpcutil.WithPrincipal(h.ctx, principal)
	lease, err := h.s.acquireControlWriteLease(ctx, j.ID, "bypass-test")
	if err != nil {
		t.Fatal(err)
	}
	in := goalProposalBody{JobSpec: job.JSON(p.ProposedSpec)}
	req := httptest.NewRequest("POST", "/v1/jobs/"+j.ID+"/goal/replans", bytes.NewReader(job.JSON(in))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", j.ID)
	req.Header.Set("X-Control-Lease", lease.LeaseToken)
	w := httptest.NewRecorder()
	h.s.httpGoalProposal(w, req)
	if w.Code < 400 || !strings.Contains(w.Body.String(), "V5_RECOVERY_PROPOSAL_REQUIRED") {
		t.Fatalf("HTTP bypass response: %d %s", w.Code, w.Body.String())
	}
}
