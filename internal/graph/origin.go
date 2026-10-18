package graph

import "github.com/tommyxie2026-tech/computecloud/internal/job"

const MemoryOriginCapability = "iu_memory_origin_v1"

type ImportIdentity struct {
	GoalID      string `json:"goal_id"`
	Generation  int64  `json:"graph_generation"`
	JobID       string `json:"job_id"`
	NodeID      string `json:"node_id"`
	ContextHash string `json:"context_sha256"`
}
type MemoryOrigin struct {
	Version    string         `json:"version"`
	Source     ImportIdentity `json:"source"`
	ViewHash   string         `json:"view_sha256"`
	RecordID   string         `json:"record_id"`
	RecordHash string         `json:"record_sha256"`
	Previous   *MemoryOrigin  `json:"previous,omitempty"`
}

func (s ImportIdentity) Valid() bool {
	return job.Ref(s.GoalID) && s.Generation > 0 && job.Ref(s.JobID) && keyPattern.MatchString(s.NodeID) && job.ValidHash(s.ContextHash)
}
func validateOrigin(origin *MemoryOrigin) error {
	depth := 0
	var previous *MemoryOrigin
	for current := origin; current != nil; current = current.Previous {
		depth++
		if depth > 8 || current.Version != "iu-record-origin-v1" || !current.Source.Valid() || !job.ValidHash(current.ViewHash) || !job.ValidHash(current.RecordHash) || !keyPattern.MatchString(current.RecordID) {
			return invalid("memory origin")
		}
		if previous != nil && (current.Source.GoalID != previous.Source.GoalID || current.Source.Generation >= previous.Source.Generation) {
			return invalid("memory origin generation")
		}
		previous = current
	}
	return nil
}
func HasMemoryOrigins(v MemoryView) bool {
	for _, r := range v.Records {
		if r.Origin != nil {
			return true
		}
	}
	for _, c := range v.Conflicts {
		for _, r := range c.Candidates {
			if r.Origin != nil {
				return true
			}
		}
	}
	return false
}

// ValidateView verifies immutable content and the negotiated memory wire shape.
func ValidateView(v MemoryView) error { return validateView(v) }
