package graph

import "fmt"

const CoverageVersion = "iu-graph-v5"

type CoverageCatalog struct {
	Version        string                 `json:"version"`
	GoalSourceID   string                 `json:"goal_source_id"`
	GoalSpecHash   string                 `json:"goal_spec_sha256"`
	Profile        string                 `json:"profile"`
	ProfileVersion string                 `json:"profile_version"`
	Scope          string                 `json:"scope"`
	Obligations    []CoverageObligation   `json:"obligations"`
	Checks         []CoverageCheckBinding `json:"checks"`
	SHA256         string                 `json:"sha256"`
}
type CoverageObligation struct {
	ID      string   `json:"constraint_id"`
	Sources []Span   `json:"sources"`
	NodeIDs []string `json:"node_ids"`
	CheckID string   `json:"check_id"`
}
type CoverageCheckBinding struct {
	ID      string   `json:"check_id"`
	Outputs []Output `json:"outputs"`
}
type CoverageGap struct {
	ConstraintID string `json:"constraint_id"`
	NodeID       string `json:"node_id"`
	SourceID     string `json:"source_id"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
	Reason       string `json:"reason"`
}
type CoverageReport struct {
	Version     string        `json:"version"`
	CatalogHash string        `json:"catalog_sha256"`
	Complete    bool          `json:"complete"`
	Gaps        []CoverageGap `json:"gaps"`
}
type CoverageError struct {
	Report CoverageReport `json:"coverage_report"`
}

func (e *CoverageError) Error() string {
	return fmt.Sprintf("COVERAGE_INSUFFICIENT: %d declared evidence gaps", len(e.Report.Gaps))
}
func (c CoverageCatalog) Digest() string { c.SHA256 = ""; return Hash(canonical(c)) }
func CheckCoverage(s Spec) (CoverageReport, error) {
	r := CoverageReport{Version: "iu-coverage-report-v1", Complete: true, Gaps: []CoverageGap{}}
	if s.Version != CoverageVersion {
		if s.Coverage != nil {
			return r, invalid("coverage version")
		}
		return r, nil
	}
	c := s.Coverage
	if c == nil || c.Version != "iu-coverage-v1" || c.SHA256 != c.Digest() || !keyPattern.MatchString(c.Profile) || !keyPattern.MatchString(c.Scope) || c.ProfileVersion == "" || len(c.ProfileVersion) > 256 || len(c.Obligations) < 1 || len(c.Obligations) > 64 || len(c.Checks) < 1 || len(c.Checks) > 32 {
		return r, invalid("coverage catalog")
	}
	r.CatalogHash = c.SHA256
	sources := map[string]Source{}
	for _, src := range s.Sources {
		src.SHA256 = Hash([]byte(src.Text))
		sources[src.ID] = src
	}
	src, ok := sources[c.GoalSourceID]
	if !ok || src.SHA256 != c.GoalSpecHash {
		return r, invalid("coverage goal source")
	}
	nodes := map[string]Node{}
	for _, n := range s.Nodes {
		nodes[n.ID] = n
	}
	outputs := map[Output]bool{}
	for _, o := range s.Outputs {
		outputs[o] = true
	}
	checks := map[string]bool{}
	for _, b := range c.Checks {
		if !keyPattern.MatchString(b.ID) || checks[b.ID] || len(b.Outputs) < 1 || len(b.Outputs) > 32 {
			return r, invalid("coverage checks")
		}
		checks[b.ID] = true
		seen := map[Output]bool{}
		for _, o := range b.Outputs {
			if !outputs[o] || seen[o] {
				return r, invalid("coverage check output")
			}
			seen[o] = true
		}
	}
	ids := map[string]bool{}
	used := map[string]bool{}
	for _, o := range c.Obligations {
		if !keyPattern.MatchString(o.ID) || ids[o.ID] || !checks[o.CheckID] || len(o.Sources) < 1 || len(o.Sources) > 32 || len(o.NodeIDs) < 1 || len(o.NodeIDs) > 32 {
			return r, invalid("coverage obligation")
		}
		ids[o.ID] = true
		used[o.CheckID] = true
		for _, sp := range o.Sources {
			if _, e := readSpan(sources, sp); e != nil {
				return r, e
			}
		}
		seen := map[string]bool{}
		for _, id := range o.NodeIDs {
			n, ok := nodes[id]
			if !ok || seen[id] {
				return r, invalid("coverage node")
			}
			seen[id] = true
			selected := append(append(append([]Span{}, s.Rules...), n.Rules...), n.Spans...)
			for _, sp := range o.Sources {
				if !requiredEvidenceCovered(sp, selected) {
					r.Complete = false
					r.Gaps = append(r.Gaps, CoverageGap{o.ID, id, sp.Source, sp.Start, sp.End, "REQUIRED_SOURCE_NOT_LOADED"})
				}
			}
		}
	}
	for _, node := range s.Nodes {
		if _, e := CoverageContextPolicy(s, node); e != nil {
			return r, e
		}
	}
	for id := range checks {
		if !used[id] {
			return r, invalid("coverage check has no obligation")
		}
	}
	return r, nil
}
func ValidateCoverage(s Spec) error {
	r, e := CheckCoverage(s)
	if e != nil {
		return e
	}
	if !r.Complete {
		return &CoverageError{r}
	}
	return nil
}

// CoverageRequiredSpans is additive validation, never automatic source loading.
func CoverageRequiredSpans(s Spec, node string) []Span {
	out := []Span{}
	if s.Coverage != nil {
		for _, o := range s.Coverage.Obligations {
			for _, id := range o.NodeIDs {
				if id == node {
					out = append(out, o.Sources...)
					break
				}
			}
		}
	}
	return out
}

func CoverageContextPolicy(s Spec, n Node) (*ContextPolicy, error) {
	if s.Coverage == nil {
		return n.ContextPolicy, nil
	}
	p := ContextPolicy{Version: "iu-context-policy-v1"}
	if n.ContextPolicy != nil {
		p = *n.ContextPolicy
	}
	all := append(append([]Span{}, p.RequiredEvidence...), CoverageRequiredSpans(s, n.ID)...)
	p.RequiredEvidence = nil
	seen := map[Span]bool{}
	for _, sp := range all {
		if !seen[sp] {
			p.RequiredEvidence = append(p.RequiredEvidence, sp)
			seen[sp] = true
		}
	}
	if e := p.Validate(); e != nil {
		return nil, e
	}
	return &p, nil
}
