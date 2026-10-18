package graph

import (
	"encoding/json"
	"sort"
)

// BindDeltaSources adds every directly presented source, never a model-selected
// minimal subset. Memory dependencies remain the complete frozen read set.
func BindDeltaSources(d Delta, spans []Span) (Delta, error) {
	direct := map[string]string{}
	for _, s := range spans {
		if !keyPattern.MatchString(s.Source) || !jobHash(s.SHA256) {
			return Delta{}, invalid("source provenance")
		}
		if h, ok := direct[s.Source]; ok && h != s.SHA256 {
			return Delta{}, invalid("source provenance conflict")
		}
		direct[s.Source] = s.SHA256
	}
	d.Records = append([]MemoryRecord{}, d.Records...)
	for i := range d.Records {
		if e := validateRecord(d.Records[i], true); e != nil {
			return Delta{}, e
		}
		sources := map[string]string{}
		for _, s := range d.Records[i].Sources {
			sources[s.ID] = s.SHA256
		}
		for id, h := range direct {
			if old, ok := sources[id]; ok && old != h {
				return Delta{}, invalid("source provenance conflict")
			}
			sources[id] = h
		}
		if len(sources) > 64 {
			return Delta{}, invalid("frozen source provenance exceeds 64; repartition or omit memory candidates")
		}
		refs := []SourceRef{}
		for id, h := range sources {
			refs = append(refs, SourceRef{id, h})
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
		d.Records[i].Sources = refs
	}
	return d, nil
}

// Frozen producer contexts let upgraded Servers invalidate legacy records that
// omitted direct source declarations. Missing provenance is never proof of validity.
func ProjectInvalidationWithContexts(view MemoryView, in InvalidationEvent, contexts map[string][]Span) (MemoryView, error) {
	if in.AuditOnly {
		return ProjectInvalidation(view, in)
	}
	next := NewView(view.Records)
	// Detach and retain every conflict candidate.
	next.Conflicts = cloneConflicts(view.Conflicts)
	ids := map[string]bool{}
	for _, id := range in.Records {
		ids[id] = true
	}
	visit := func(r *MemoryRecord) {
		spans, known := contexts[r.InputHash]
		if !known && len(in.Sources)+len(in.Records) > 0 && r.Validity == "VALID" {
			r.Validity = "NEEDS_REVALIDATION"
		}
		for _, span := range spans {
			for _, target := range in.Sources {
				if span.Source == target.ID && span.SHA256 == target.SHA256 {
					ids[r.ID] = true
				}
			}
		}
	}
	for i := range next.Records {
		visit(&next.Records[i])
	}
	for i := range next.Conflicts {
		for k := range next.Conflicts[i].Candidates {
			visit(&next.Conflicts[i].Candidates[k])
		}
	}
	projectValidity(&next)
	next = sealView(next)
	in.Records = nil
	for id := range ids {
		in.Records = append(in.Records, id)
	}
	sort.Strings(in.Records)
	return ProjectInvalidation(next, in)
}

func cloneConflicts(in []MemoryConflict) []MemoryConflict {
	var out []MemoryConflict
	_ = json.Unmarshal(canonical(in), &out)
	return out
}
