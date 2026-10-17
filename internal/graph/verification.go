package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"math"
	"strconv"
	"strings"
)

type RecordVerifierProfile struct {
	Version       string   `json:"version" yaml:"version"`
	Kind          string   `json:"kind" yaml:"kind"`
	Scope         string   `json:"scope" yaml:"scope"`
	SourcePointer string   `json:"source_pointer" yaml:"source_pointer"`
	ClaimPointer  string   `json:"claim_pointer" yaml:"claim_pointer"`
	RecordKinds   []string `json:"record_kinds" yaml:"record_kinds"`
	TimeoutMS     int      `json:"timeout_ms" yaml:"timeout_ms"`
}

func (p RecordVerifierProfile) Valid() bool {
	return job.Ref(p.Version) && keyPattern.MatchString(p.Scope) && (p.Kind == "source_json_equal" || p.Kind == "unavailable") && validPointer(p.SourcePointer) && validPointer(p.ClaimPointer) && len(p.RecordKinds) > 0 && len(p.RecordKinds) <= 6 && p.TimeoutMS > 0 && p.TimeoutMS <= 1000
}
func validPointer(s string) bool {
	if len(s) > 512 || s != "" && !strings.HasPrefix(s, "/") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' {
			if i+1 >= len(s) || s[i+1] != '0' && s[i+1] != '1' {
				return false
			}
			i++
		}
	}
	return true
}
func pointerValue(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	for _, key := range strings.Split(pointer[1:], "/") {
		key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[key]
			if !ok {
				return nil, false
			}
		case []any:
			i, e := strconv.Atoi(key)
			if e != nil || i < 0 || i >= len(v) || strconv.Itoa(i) != key {
				return nil, false
			}
			value = v[i]
		default:
			return nil, false
		}
	}
	return value, true
}
func preciseJSON(raw []byte) (any, error) {
	if e := jsonutil.Validate(raw); e != nil {
		return nil, e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var out any
	e := d.Decode(&out)
	return out, e
}

type RecordVerificationResult struct {
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason"`
	EvidenceHash string `json:"evidence_sha256"`
}

func RunRecordVerifier(ctx context.Context, p RecordVerifierProfile, source Source, r MemoryRecord) RecordVerificationResult {
	out := RecordVerificationResult{Verdict: "UNKNOWN", Reason: "VERIFIER_UNAVAILABLE"}
	if !p.Valid() || ctx.Err() != nil || p.Kind == "unavailable" {
		return out
	}
	allowed := false
	for _, kind := range p.RecordKinds {
		if r.Kind == kind {
			allowed = true
		}
	}
	if !allowed {
		return out
	}
	if source.SHA256 != Hash([]byte(source.Text)) || r.Validity != "VALID" {
		out.Reason = "STALE_EVIDENCE"
		return out
	}
	original, e := preciseJSON([]byte(source.Text))
	if e != nil {
		out.Reason = "SOURCE_AMBIGUOUS"
		return out
	}
	claim, e := preciseJSON(r.Payload)
	if e != nil {
		out.Reason = "CLAIM_AMBIGUOUS"
		return out
	}
	a, ok := pointerValue(original, p.SourcePointer)
	if !ok {
		out.Reason = "SOURCE_FIELD_MISSING"
		return out
	}
	b, ok := pointerValue(claim, p.ClaimPointer)
	if !ok {
		out.Reason = "CLAIM_FIELD_MISSING"
		return out
	}
	if ctx.Err() != nil {
		out.Reason = "VERIFIER_TIMEOUT"
		return out
	}
	out.EvidenceHash = Hash(canonical(struct {
		Profile                 RecordVerifierProfile
		Source                  SourceRef
		ClaimHash               string
		SourceValue, ClaimValue any
	}{p, SourceRef{source.ID, source.SHA256}, Hash(r.Payload), a, b}))
	out.Verdict = "FAIL"
	out.Reason = "SOURCE_FIELD_DIFFERS"
	if bytes.Equal(canonical(a), canonical(b)) {
		out.Verdict = "PASS"
		out.Reason = "SOURCE_FIELD_EQUAL"
	}
	return out
}

type EmpiricalEvidence struct {
	EvaluationProfile string   `json:"evaluation_profile"`
	EvaluationVersion string   `json:"evaluation_version"`
	DatasetHash       string   `json:"dataset_sha256"`
	Samples           int      `json:"samples"`
	Successes         int      `json:"successes"`
	Lower95           *float64 `json:"wilson_lower_95"`
	Upper95           *float64 `json:"wilson_upper_95"`
}

func (p *EmpiricalEvidence) Calibrate() error {
	p.Lower95 = nil
	p.Upper95 = nil
	if !job.Ref(p.EvaluationProfile) || !job.Ref(p.EvaluationVersion) || !job.ValidHash(p.DatasetHash) || p.Samples < 0 || p.Successes < 0 || p.Successes > p.Samples {
		return invalid("empirical evidence")
	}
	if p.Samples == 0 {
		return nil
	}
	n := float64(p.Samples)
	rate := float64(p.Successes) / n
	z := 1.959963984540054
	z2 := z * z
	den := 1 + z2/n
	center := (rate + z2/(2*n)) / den
	radius := z * math.Sqrt(rate*(1-rate)/n+z2/(4*n*n)) / den
	lo, hi := math.Max(0, center-radius), math.Min(1, center+radius)
	p.Lower95 = &lo
	p.Upper95 = &hi
	return nil
}

// ProjectVerifiedScope retains all records/conflicts and changes validation only
// in this explicitly scoped detached view. It never authorizes a caller.
func ProjectVerifiedScope(original MemoryView, ids []string) (MemoryView, error) {
	if e := validateView(original); e != nil {
		return MemoryView{}, e
	}
	var out MemoryView
	if e := json.Unmarshal(canonical(original), &out); e != nil {
		return out, e
	}
	selected := map[string]bool{}
	for _, id := range ids {
		if selected[id] {
			return MemoryView{}, invalid("duplicate verification target")
		}
		selected[id] = true
	}
	for _, c := range out.Conflicts {
		if selected[c.RecordID] {
			return MemoryView{}, invalid("verification conflict")
		}
	}
	for i := range out.Records {
		r := &out.Records[i]
		r.Validation = "unchecked"
		if selected[r.ID] {
			if r.Validity != "VALID" {
				return MemoryView{}, invalid("verification invalid record")
			}
			r.Validation = "verified"
			delete(selected, r.ID)
		}
	}
	if len(selected) > 0 {
		return MemoryView{}, invalid("verification target missing")
	}
	for i := range out.Conflicts {
		for k := range out.Conflicts[i].Candidates {
			out.Conflicts[i].Candidates[k].Validation = "unchecked"
		}
	}
	return sealView(out), nil
}
