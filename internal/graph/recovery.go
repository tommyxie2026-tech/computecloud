package graph

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type RecoveryProposal struct {
	Version             string   `json:"version"`
	ID                  string   `json:"id"`
	GoalID              string   `json:"goal_id"`
	PriorGeneration     int64    `json:"prior_generation"`
	PriorPlanHash       string   `json:"prior_plan_sha256"`
	EvaluationID        string   `json:"evaluation_id"`
	EvaluationHash      string   `json:"evaluation_sha256"`
	CoverageReceiptID   string   `json:"coverage_receipt_id"`
	CoverageReceiptHash string   `json:"coverage_receipt_sha256"`
	ReadReceiptIDs      []string `json:"read_receipt_ids"`
	ProposedSpec        Spec     `json:"proposed_spec"`
	SHA256              string   `json:"sha256"`
}

func (p RecoveryProposal) Digest() string { p.SHA256 = ""; return Hash(canonical(p)) }
func ValidateRecovery(prior, next Spec) error {
	if prior.Version != CoverageVersion || next.Version != CoverageVersion || len(prior.Nodes) != len(next.Nodes) {
		return invalid("recovery version or nodes")
	}
	var p, n Spec
	if json.Unmarshal(canonical(prior), &p) != nil || json.Unmarshal(canonical(next), &n) != nil {
		return invalid("recovery encoding")
	}
	old := map[string]Node{}
	for _, v := range p.Nodes {
		old[v.ID] = v
	}
	changed := false
	for i, v := range n.Nodes {
		base, ok := old[v.ID]
		if !ok || !utf8.ValidString(v.Instructions) || !strings.HasPrefix(v.Instructions, base.Instructions) || len(v.Instructions)-len(base.Instructions) > 8<<10 {
			return invalid("recovery instructions")
		}
		if v.Instructions != base.Instructions {
			changed = true
		}
		for _, pair := range [][2][]Span{{base.Spans, v.Spans}, {base.Rules, v.Rules}} {
			for _, sp := range pair[0] {
				if !requiredEvidenceCovered(sp, pair[1]) {
					return invalid("recovery removes required source")
				}
			}
			for _, sp := range pair[1] {
				if !requiredEvidenceCovered(sp, pair[0]) {
					changed = true
				}
			}
		}
		n.Nodes[i].Instructions = base.Instructions
		n.Nodes[i].Spans = base.Spans
		n.Nodes[i].Rules = base.Rules
	}
	if !changed || string(canonical(p)) != string(canonical(n)) {
		return invalid("recovery unsupported change")
	}
	if _, e := Compile(next); e != nil {
		return e
	}
	return nil
}
