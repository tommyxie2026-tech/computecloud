package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/server"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"net/url"
	"os"
)

type RecoveryRead struct {
	NodeID string     `json:"node_id"`
	Span   graph.Span `json:"span"`
}
type RecoveryRecipe struct {
	Version        string            `json:"version"`
	ID             string            `json:"id"`
	PriorPlanHash  string            `json:"prior_plan_sha256"`
	EvaluationID   string            `json:"evaluation_id"`
	EvaluationHash string            `json:"evaluation_sha256"`
	Reads          []RecoveryRead    `json:"reads"`
	Instructions   map[string]string `json:"instructions"`
	Approval       string            `json:"approval_operation_id,omitempty"`
}
type recoveryRead func(string, string, []byte) ([]byte, error)

func runRecoveryCLI(ctx context.Context, id, file string, read recoveryRead, setLease func(string)) error {
	raw, e := os.ReadFile(file)
	if e != nil {
		return e
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("RECOVERY_RECIPE_LIMIT")
	}
	var recipe RecoveryRecipe
	if e = jsonutil.Decode(raw, &recipe); e != nil {
		return e
	}
	if recipe.Version != "iu-recovery-recipe-v1" || !job.ValidKey(recipe.ID) {
		return fmt.Errorf("RECOVERY_RECIPE_INVALID")
	}
	path := "/v1/jobs/" + url.PathEscape(id)
	get := func(endpoint string, target any) error {
		raw, e := read("GET", endpoint, nil)
		if e != nil {
			return e
		}
		return jsonutil.Decode(raw, target)
	}
	var st server.GraphStatus
	if e = get(path+"/graph", &st); e != nil {
		return e
	}
	if st.Plan.Digest != recipe.PriorPlanHash || st.EvaluationID != recipe.EvaluationID || graph.Hash(st.Evaluation) != recipe.EvaluationHash {
		return fmt.Errorf("RECOVERY_RECIPE_STALE")
	}
	var ev server.GraphEvaluation
	if e = jsonutil.Decode(st.Evaluation, &ev); e != nil || ev.Verdict != "FAIL" || ev.CoverageProof == nil {
		return fmt.Errorf("RECOVERY_TRUSTED_FAIL_REQUIRED")
	}
	var proof server.GraphCoverageReceipt
	if e = get(path+"/graph/coverage-verifications/"+url.PathEscape(ev.CoverageProof.ReceiptID), &proof); e != nil {
		return e
	}
	if proof.Verdict != "FAIL" || proof.GoalID != st.GoalID || proof.Generation != st.Generation || graph.Hash(job.JSON(proof)) != ev.CoverageProof.SHA256 {
		return fmt.Errorf("RECOVERY_PROOF_MISMATCH")
	}
	if len(recipe.Reads) < 1 || len(recipe.Reads) > 8 {
		return fmt.Errorf("RECOVERY_READ_LIMIT")
	}
	var next graph.Spec
	if e = json.Unmarshal(job.JSON(st.Plan.Spec), &next); e != nil {
		return e
	}
	nodes := map[string]int{}
	for i, n := range next.Nodes {
		nodes[n.ID] = i
	}
	for node, appendText := range recipe.Instructions {
		i, ok := nodes[node]
		if !ok {
			return fmt.Errorf("RECOVERY_NODE_UNKNOWN")
		}
		next.Nodes[i].Instructions += appendText
	}
	p := graph.RecoveryProposal{Version: "iu-recovery-proposal-v1", ID: recipe.ID, GoalID: st.GoalID, PriorGeneration: st.Generation, PriorPlanHash: st.Plan.Digest, EvaluationID: st.EvaluationID, EvaluationHash: recipe.EvaluationHash, CoverageReceiptID: ev.CoverageProof.ReceiptID, CoverageReceiptHash: ev.CoverageProof.SHA256, ProposedSpec: next}
	total := 0
	for _, wanted := range recipe.Reads {
		if e = ctx.Err(); e != nil {
			return e
		}
		i, ok := nodes[wanted.NodeID]
		if !ok {
			return fmt.Errorf("RECOVERY_NODE_UNKNOWN")
		}
		stableID := "recovery-" + graph.Hash(job.JSON(struct {
			ID   string
			Read RecoveryRead
		}{recipe.ID, wanted}))[:48]
		var existing server.GraphSourceReadStatus
		if e = get(path+"/graph/source-reads/"+url.PathEscape(wanted.NodeID), &existing); e != nil {
			return e
		}
		var receipt *server.GraphSourceReadReceipt
		for _, entry := range existing.Reads {
			if entry.Request.ID == stableID {
				if entry.Request.NodeID != wanted.NodeID || entry.Request.PlanHash != st.Plan.Digest || entry.Request.Span != wanted.Span {
					return fmt.Errorf("RECOVERY_READ_CONFLICT")
				}
				r := entry.Receipt
				receipt = &r
				break
			}
		}
		if receipt == nil {
			req := server.GraphSourceReadRequest{ID: stableID, NodeID: wanted.NodeID, PlanHash: st.Plan.Digest, ExpectedReads: existing.ReadsUsed, Span: wanted.Span}
			raw, e := read("POST", path+"/graph/source-reads", job.JSON(req))
			if e != nil {
				return e
			}
			var r server.GraphSourceReadReceipt
			if e = jsonutil.Decode(raw, &r); e != nil {
				return e
			}
			receipt = &r
		}
		if receipt.GoalID != st.GoalID || receipt.Generation != st.Generation || receipt.NodeID != wanted.NodeID || receipt.PlanHash != st.Plan.Digest || receipt.Span != wanted.Span || receipt.SHA256 != graph.Hash([]byte(receipt.Text)) {
			return fmt.Errorf("RECOVERY_READ_MISMATCH")
		}
		total += len(receipt.Text)
		if total > 64<<10 {
			return fmt.Errorf("RECOVERY_READ_LIMIT")
		}
		p.ReadReceiptIDs = append(p.ReadReceiptIDs, receipt.ReceiptID)
		present := false
		for _, sp := range p.ProposedSpec.Nodes[i].Spans {
			if sp == wanted.Span {
				present = true
			}
		}
		if !present {
			p.ProposedSpec.Nodes[i].Spans = append(p.ProposedSpec.Nodes[i].Spans, wanted.Span)
		}
	}
	if e = graph.ValidateRecovery(st.Plan.Spec, p.ProposedSpec); e != nil {
		return e
	}
	p.SHA256 = p.Digest()
	holder := job.JSON(map[string]string{"holder_id": "cli-recovery-" + store.ID()})
	raw, e = read("POST", path+"/control-lease", holder)
	if e != nil {
		return e
	}
	var lease struct {
		Token string `json:"lease_token"`
	}
	if e = jsonutil.Decode(raw, &lease); e != nil || lease.Token == "" {
		return fmt.Errorf("INVALID_CONTROL_LEASE")
	}
	setLease(lease.Token)
	defer func() { _, _ = read("DELETE", path+"/control-lease", holder); setLease("") }()
	raw, e = read("POST", path+"/graph/recovery-proposals", job.JSON(struct {
		Proposal graph.RecoveryProposal `json:"proposal"`
		Approval string                 `json:"approval_operation_id"`
	}{p, recipe.Approval}))
	if e != nil {
		return e
	}
	_, e = fmt.Fprintln(os.Stdout, string(raw))
	return e
}
