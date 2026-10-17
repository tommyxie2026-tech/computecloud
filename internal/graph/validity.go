package graph

import (
	"fmt"
	"sort"
)

type InvalidationEvent struct {
	ID               string      `json:"id"`
	GoalID           string      `json:"goal_id"`
	Generation       int64       `json:"graph_generation"`
	ExpectedRevision int64       `json:"expected_revision"`
	Reason           string      `json:"reason"`
	EvidenceHash     string      `json:"evidence_sha256"`
	Sources          []SourceRef `json:"sources,omitempty"`
	Records          []string    `json:"record_ids,omitempty"`
	AuditOnly        bool        `json:"audit_only,omitempty"`
}

func (in InvalidationEvent) Validate() error {
	if !keyPattern.MatchString(in.ID) || in.GoalID == "" || in.Generation < 1 || in.ExpectedRevision < 0 || !jobHash(in.EvidenceHash) || len(in.Sources) > 32 || len(in.Records) > 32 || len(in.Sources)+len(in.Records) == 0 {
		return fmt.Errorf("INVALIDATION_INVALID")
	}
	if in.Reason != "RULE_CHANGED" && in.Reason != "SOURCE_REVISED" && in.Reason != "COUNTEREVIDENCE" {
		return fmt.Errorf("INVALIDATION_REASON_INVALID")
	}
	seen := map[string]bool{}
	for _, s := range in.Sources {
		if !keyPattern.MatchString(s.ID) || !jobHash(s.SHA256) || seen[s.ID] {
			return fmt.Errorf("INVALIDATION_SOURCE_INVALID")
		}
		seen[s.ID] = true
	}
	seen = map[string]bool{}
	for _, id := range in.Records {
		if !keyPattern.MatchString(id) || seen[id] {
			return fmt.Errorf("INVALIDATION_RECORD_INVALID")
		}
		seen[id] = true
	}
	return nil
}
func ProjectInvalidation(view MemoryView, in InvalidationEvent) (MemoryView, error) {
	if e := validateView(view); e != nil {
		return MemoryView{}, e
	}
	if in.AuditOnly {
		return view, nil
	}
	ids := map[string]bool{}
	all := append([]MemoryRecord(nil), view.Records...)
	for _, c := range view.Conflicts {
		all = append(all, c.Candidates...)
	}
	for _, r := range all {
		for _, id := range in.Records {
			if r.ID == id {
				ids[r.ID] = true
			}
		}
		for _, src := range r.Sources {
			for _, target := range in.Sources {
				if src == target {
					ids[r.ID] = true
				}
			}
		}
	}
	roots := make([]string, 0, len(ids))
	for id := range ids {
		roots = append(roots, id)
	}
	sort.Strings(roots)
	return Invalidate(view, roots)
}
