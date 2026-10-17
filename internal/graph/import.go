package graph

// ImportedMemory is a detached, unchecked view and an explicit origin mapping.
// Namespace must include the authorized grant and target generation identity.
type ImportedMemory struct {
	View           MemoryView        `json:"view"`
	OriginViewHash string            `json:"origin_view_sha256"`
	IDs            map[string]string `json:"record_ids"`
}

func ImportView(origin MemoryView, ids []string, namespace string, source ImportIdentity) (ImportedMemory, error) {
	var out ImportedMemory
	if e := validateView(origin); e != nil {
		return out, e
	}
	if !source.Valid() || namespace == "" || len(namespace) > 512 || len(ids) < 1 || len(ids) > 32 {
		return out, invalid("import bounds")
	}
	selected := map[string]bool{}
	records := map[string]MemoryRecord{}
	for _, r := range origin.Records {
		records[r.ID] = r
	}
	for _, id := range ids {
		r, ok := records[id]
		if !ok || selected[id] || r.Validity != "VALID" {
			return out, invalid("import record unavailable")
		}
		selected[id] = true
	}
	for _, c := range origin.Conflicts {
		if selected[c.RecordID] {
			return out, invalid("import conflict")
		}
	}
	out.IDs = map[string]string{}
	for id := range selected {
		r := records[id]
		for _, dep := range append(append([]string{}, r.Dependencies...), r.Supersedes...) {
			if !selected[dep] {
				return ImportedMemory{}, invalid("import dependency closure incomplete")
			}
		}
		out.IDs[id] = "i_" + Hash([]byte(namespace + "\x00" + id))[:60]
	}
	var result []MemoryRecord
	// NewView below detaches all slices from the immutable origin.
	for id := range selected {
		r := records[id]
		r.Origin = &MemoryOrigin{Version: "iu-record-origin-v1", Source: source, ViewHash: origin.SHA256, RecordID: id, RecordHash: Hash(canonical(r)), Previous: r.Origin}
		if e := validateOrigin(r.Origin); e != nil {
			return ImportedMemory{}, e
		}
		r.ID = out.IDs[id]
		r.Validation = "unchecked"
		r.Dependencies = append([]string{}, r.Dependencies...)
		r.Supersedes = append([]string{}, r.Supersedes...)
		for i, dep := range r.Dependencies {
			r.Dependencies[i] = out.IDs[dep]
		}
		for i, dep := range r.Supersedes {
			r.Supersedes[i] = out.IDs[dep]
		}
		result = append(result, r)
	}
	out.View = NewView(result)
	out.OriginViewHash = origin.SHA256
	return out, validateView(out.View)
}
