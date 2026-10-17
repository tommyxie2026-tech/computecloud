package graph

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
)

type SourceRef struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}
type MemoryRecord struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Payload      json.RawMessage `json:"payload"`
	Sources      []SourceRef     `json:"sources,omitempty"`
	Dependencies []string        `json:"depends_on,omitempty"`
	Supersedes   []string        `json:"supersedes,omitempty"`
	Validation   string          `json:"validation"`
	Validity     string          `json:"validity"`
	InputHash    string          `json:"input_sha256,omitempty"`
}
type MemoryConflict struct {
	RecordID   string         `json:"record_id"`
	Candidates []MemoryRecord `json:"candidates"`
}
type MemoryView struct {
	Version   string           `json:"version"`
	Records   []MemoryRecord   `json:"records"`
	Conflicts []MemoryConflict `json:"conflicts,omitempty"`
	SHA256    string           `json:"sha256"`
}
type Delta struct {
	Version      string         `json:"version"`
	ReadViewHash string         `json:"read_view_sha256"`
	ContextHash  string         `json:"context_sha256"`
	Records      []MemoryRecord `json:"records"`
}

func EmptyView() MemoryView { return NewView(nil) }
func NewView(records []MemoryRecord) MemoryView {
	// Round-trip detaches payloads, sources and dependency slices from old views.
	var cloned []MemoryRecord
	_ = json.Unmarshal(canonical(records), &cloned)
	if cloned == nil {
		cloned = []MemoryRecord{}
	}
	for i := range cloned {
		sort.Strings(cloned[i].Dependencies)
		sort.Strings(cloned[i].Supersedes)
		sort.Slice(cloned[i].Sources, func(a, b int) bool { return cloned[i].Sources[a].ID < cloned[i].Sources[b].ID })
	}
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].ID < cloned[j].ID })
	return sealView(MemoryView{Version: "iu-memory-view-v1", Records: cloned})
}
func sealView(v MemoryView) MemoryView { v.SHA256 = ""; v.SHA256 = Hash(canonical(v)); return v }
func validateView(v MemoryView) error {
	if v.Version != "iu-memory-view-v1" || v.SHA256 != sealView(v).SHA256 {
		return invalid("memory view hash")
	}
	records := map[string]MemoryRecord{}
	for _, r := range v.Records {
		if _, ok := records[r.ID]; ok {
			return invalid("duplicate record")
		}
		if e := validateRecord(r, false); e != nil {
			return e
		}
		records[r.ID] = r
	}
	visiting := map[string]bool{}
	done := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if done[id] {
			return nil
		}
		r, ok := records[id]
		if !ok {
			return invalid("missing memory dependency")
		}
		if visiting[id] {
			return invalid("memory dependency cycle")
		}
		visiting[id] = true
		for _, dep := range r.Dependencies {
			if e := visit(dep); e != nil {
				return e
			}
		}
		visiting[id] = false
		done[id] = true
		return nil
	}
	for id := range records {
		if e := visit(id); e != nil {
			return e
		}
	}
	return nil
}
func validateRecord(r MemoryRecord, candidate bool) error {
	if !keyPattern.MatchString(r.ID) || len(r.Payload) > 8<<10 || !json.Valid(r.Payload) || len(r.Sources) > 64 || len(r.Dependencies) > 1024 || len(r.Supersedes) > 64 {
		return invalid("memory record bounds")
	}
	switch r.Kind {
	case "observation", "hypothesis", "decision", "constraint", "unresolved", "result":
	default:
		return invalid("memory kind")
	}
	if candidate && r.Validation != "unchecked" || r.Validation != "unchecked" && r.Validation != "verified" {
		return invalid("memory verification")
	}
	if r.Validity != "VALID" && r.Validity != "INVALID" && r.Validity != "NEEDS_REVALIDATION" {
		return invalid("memory validity")
	}
	seen := map[string]bool{}
	for _, src := range r.Sources {
		if src.ID == "" || !jobHash(src.SHA256) || seen[src.ID] {
			return invalid("memory source")
		}
		seen[src.ID] = true
	}
	return nil
}
func jobHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func ValidateDelta(raw []byte, view MemoryView) (Delta, error) {
	var d Delta
	if len(raw) > 64<<10 {
		return d, invalid("memory delta size")
	}
	if e := jsonutil.Decode(raw, &d); e != nil {
		return d, e
	}
	if e := validateView(view); e != nil {
		return d, e
	}
	if d.Version != "iu-memory-delta-v1" || d.ReadViewHash != view.SHA256 || !jobHash(d.ContextHash) || len(d.Records) > 32 {
		return d, invalid("memory delta contract")
	}
	available := map[string]bool{}
	for _, r := range view.Records {
		available[r.ID] = true
	}
	seen := map[string]bool{}
	for i := range d.Records {
		r := &d.Records[i]
		if e := validateRecord(*r, true); e != nil {
			return d, e
		}
		if seen[r.ID] || available[r.ID] {
			return d, invalid("duplicate new memory identity")
		}
		seen[r.ID] = true
		for _, dep := range append(append([]string{}, r.Dependencies...), r.Supersedes...) {
			if !available[dep] {
				return d, invalid("undeclared memory dependency")
			}
		}
		// Conservative lineage: every result depends on the entire presented read set.
		r.Dependencies = nil
		for id := range available {
			r.Dependencies = append(r.Dependencies, id)
		}
		sort.Strings(r.Dependencies)
		r.InputHash = d.ContextHash
		r.Validity = "VALID"
	}
	return d, nil
}

func ApplyDelta(view MemoryView, d Delta) (MemoryView, error) {
	checked, e := ValidateDelta(canonical(d), view)
	if e != nil {
		return MemoryView{}, e
	}
	records := append(append([]MemoryRecord{}, view.Records...), checked.Records...)
	next := NewView(records)
	for _, r := range checked.Records {
		if len(r.Supersedes) > 0 {
			next, e = Invalidate(next, r.Supersedes)
			if e != nil {
				return MemoryView{}, e
			}
		}
	}
	return next, nil
}

// MergeViews retains all conflicting candidates and uses stable ordering, never arrival order.
func MergeViews(views []MemoryView) (MemoryView, error) {
	variants := map[string]map[string]MemoryRecord{}
	for _, v := range views {
		if e := validateView(v); e != nil {
			return MemoryView{}, e
		}
		for _, r := range v.Records {
			if variants[r.ID] == nil {
				variants[r.ID] = map[string]MemoryRecord{}
			}
			variants[r.ID][string(canonical(r))] = r
		}
		for _, c := range v.Conflicts {
			for _, r := range c.Candidates {
				if variants[r.ID] == nil {
					variants[r.ID] = map[string]MemoryRecord{}
				}
				variants[r.ID][string(canonical(r))] = r
			}
		}
	}
	var records []MemoryRecord
	var conflicts []MemoryConflict
	for id, candidates := range variants {
		keys := []string{}
		for k := range candidates {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		r := candidates[keys[0]]
		if len(keys) > 1 {
			c := MemoryConflict{RecordID: id}
			for _, k := range keys {
				c.Candidates = append(c.Candidates, candidates[k])
			}
			conflicts = append(conflicts, c)
			r.Validity = "NEEDS_REVALIDATION"
			r.Validation = "unchecked"
		}
		records = append(records, r)
	}
	v := NewView(records)
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].RecordID < conflicts[j].RecordID })
	v.Conflicts = conflicts
	// Propagate uncertainty to all derived conclusions, without validating them.
	changed := true
	for changed {
		changed = false
		for i := range v.Records {
			r := &v.Records[i]
			for _, dep := range r.Dependencies {
				for _, parent := range v.Records {
					if parent.ID == dep && parent.Validity != "VALID" && r.Validity == "VALID" {
						r.Validity = parent.Validity
						changed = true
					}
				}
			}
		}
	}
	v = sealView(v)
	if e := validateView(v); e != nil {
		return MemoryView{}, e
	}
	return v, nil
}

func Invalidate(view MemoryView, ids []string) (MemoryView, error) {
	if e := validateView(view); e != nil {
		return MemoryView{}, e
	}
	next := NewView(view.Records)
	next.Conflicts = append([]MemoryConflict{}, view.Conflicts...)
	invalidIDs := map[string]bool{}
	for _, id := range ids {
		found := false
		for _, r := range next.Records {
			if r.ID == id {
				found = true
			}
		}
		if !found {
			return MemoryView{}, fmt.Errorf("MEMORY_DEPENDENCY_MISSING: %s", id)
		}
		invalidIDs[id] = true
	}
	changed := true
	for changed {
		changed = false
		for _, r := range next.Records {
			for _, dep := range r.Dependencies {
				if invalidIDs[dep] && !invalidIDs[r.ID] {
					invalidIDs[r.ID] = true
					changed = true
				}
			}
		}
	}
	for i := range next.Records {
		if invalidIDs[next.Records[i].ID] {
			next.Records[i].Validity = "INVALID"
		}
	}
	return sealView(next), nil
}
