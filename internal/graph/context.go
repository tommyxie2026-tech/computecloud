package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

type InputValue struct {
	SourcePort string `json:"source_port,omitempty"`
	Node       string `json:"node"`
	Port       string `json:"port"`
	ArtifactID string `json:"artifact_id"`
	SHA256     string `json:"sha256"`
	Value      string `json:"value"`
}
type ContextRequest struct {
	Policy       *ContextPolicy
	OutputSchema json.RawMessage
	Parameters   json.RawMessage
	Sources      []Source
	Rules        []Span
	Spans        []Span
	Instructions string
	View         MemoryView
	Inputs       []InputValue
	Model        string
}
type ContextManifest struct {
	Version          string `json:"version"`
	Text             string `json:"text"`
	SHA256           string `json:"sha256"`
	ReadViewHash     string `json:"read_view_sha256"`
	InputFingerprint string `json:"input_fingerprint"`
	Spans            []Span `json:"spans"`
	Model            string `json:"model"`
	TokenCount       *int64 `json:"token_count"`
	TokenCoverage    string `json:"token_coverage"`
}
type contextSpan struct {
	Role         string `json:"role,omitempty"`
	MessageIndex int    `json:"message_index"`
	Span         Span   `json:"source"`
	Text         string `json:"text"`
}

// CompileContext preserves every required segment or returns an explicit error.
// The bytes of Text are the actual serialized model input, not an estimate.
func CompileContext(r ContextRequest) (ContextManifest, error) {
	var m ContextManifest
	if !utf8.ValidString(r.Instructions) || r.Model == "" {
		return m, fmt.Errorf("CONTEXT_INVALID")
	}
	sources := map[string]Source{}
	for _, src := range r.Sources {
		if _, exists := sources[src.ID]; exists || !utf8.ValidString(src.Text) {
			return m, invalid("source")
		}
		h := Hash([]byte(src.Text))
		if src.SHA256 != "" && src.SHA256 != h {
			return m, invalid("source hash")
		}
		src.SHA256 = h
		sources[src.ID] = src
	}
	if e := validateView(r.View); e != nil {
		return m, e
	}
	maxBytes := 64 << 10
	if r.Policy != nil {
		if e := r.Policy.Validate(); e != nil {
			return m, e
		}
		if r.Policy.MaxBytes > 0 {
			maxBytes = r.Policy.MaxBytes
		}
		for _, required := range r.Policy.RequiredEvidence {
			if _, e := readSpan(sources, required); e != nil {
				return m, fmt.Errorf("CONTEXT_INSUFFICIENT: required evidence: %w", e)
			}
			if !requiredEvidenceCovered(required, append(append([]Span{}, r.Rules...), r.Spans...)) {
				return m, fmt.Errorf("CONTEXT_INSUFFICIENT: required evidence missing")
			}
		}
	}
	var rules, local []contextSpan
	for kind, spans := range [][]Span{r.Rules, r.Spans} {
		for _, span := range spans {
			text, e := readSpan(sources, span)
			if e != nil {
				return m, fmt.Errorf("CONTEXT_INSUFFICIENT: %w", e)
			}
			segment := contextSpan{Span: span, Text: text, Role: sources[span.Source].Role, MessageIndex: sources[span.Source].MessageIndex}
			if kind == 0 {
				rules = append(rules, segment)
			} else {
				local = append(local, segment)
			}
		}
	}
	body := struct {
		Version      string          `json:"version"`
		Instructions string          `json:"instructions"`
		Rules        []contextSpan   `json:"rules"`
		Local        []contextSpan   `json:"local"`
		Memory       MemoryView      `json:"memory"`
		Inputs       []InputValue    `json:"inputs"`
		Parameters   json.RawMessage `json:"parameters,omitempty"`
		OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	}{"iu-context-v1", r.Instructions, rules, local, r.View, r.Inputs, r.Parameters, r.OutputSchema}
	for _, in := range r.Inputs {
		if !utf8.ValidString(in.Value) || in.SHA256 != Hash([]byte(in.Value)) {
			return m, invalid("context input hash")
		}
	}
	raw := canonical(body)
	if len(raw) > maxBytes {
		return m, fmt.Errorf("CONTEXT_INSUFFICIENT: serialized input exceeds byte policy")
	}
	m = ContextManifest{Version: "iu-context-v1", Text: string(raw), SHA256: Hash(raw), ReadViewHash: r.View.SHA256, InputFingerprint: Hash(canonical(struct {
		Body  any
		Model string
	}{body, r.Model})), Spans: append(append([]Span{}, r.Rules...), r.Spans...), Model: r.Model, TokenCoverage: "unknown"}
	return m, nil
}

// ReconstructSource checks complete coverage; overlapping spans never duplicate bytes.
func ReconstructSource(src Source, spans []Span) (string, error) {
	h := Hash([]byte(src.Text))
	if src.SHA256 != "" && src.SHA256 != h {
		return "", invalid("source hash")
	}
	src.SHA256 = h
	ordered := append([]Span{}, spans...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	end := 0
	for _, span := range ordered {
		if _, e := readSpan(map[string]Source{src.ID: src}, span); e != nil {
			return "", e
		}
		if span.Start > end {
			return "", invalid("source coverage gap")
		}
		if span.End > end {
			end = span.End
		}
	}
	if end != len(src.Text) {
		return "", invalid("source coverage incomplete")
	}
	return src.Text, nil
}
