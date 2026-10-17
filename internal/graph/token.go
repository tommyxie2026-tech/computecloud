package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"sort"
)

type ContextPolicy struct {
	Version          string `json:"version"`
	MaxBytes         int    `json:"max_bytes,omitempty"`
	MaxInputTokens   *int64 `json:"max_input_tokens,omitempty"`
	RequiredEvidence []Span `json:"required_evidence,omitempty"`
}

func (p ContextPolicy) Validate() error {
	if p.Version != "iu-context-policy-v1" || p.MaxBytes < 0 || p.MaxBytes > 64<<10 || len(p.RequiredEvidence) > 128 {
		return invalid("context policy")
	}
	if p.MaxInputTokens != nil {
		return fmt.Errorf("CONTEXT_TOKEN_BUDGET_UNSUPPORTED")
	}
	return nil
}

type InputCountRequest struct {
	ModelID          string `json:"model_id"`
	TokenizerVersion string `json:"tokenizer_version"`
	TemplateVersion  string `json:"template_version"`
	AdapterVersion   string `json:"adapter_version"`
	Coverage         string `json:"coverage"`
	SerializedInput  string `json:"-"`
}
type InputCountReceipt struct {
	Version     string `json:"version"`
	BindingHash string `json:"binding_sha256"`
	InputHash   string `json:"input_sha256"`
	Count       *int64 `json:"count"`
	Coverage    string `json:"coverage"`
}

// InputCounter must count its pinned tokenizer and honor context cancellation.
// CLI-hidden system prompts are outside serialized_prompt coverage. There is no
// production byte/rune-count fallback and this receipt never changes Usage.
type InputCounter func(context.Context, InputCountRequest) (int64, error)

func CountInput(ctx context.Context, in InputCountRequest, counter InputCounter) (InputCountReceipt, error) {
	out := InputCountReceipt{Version: "iu-input-count-v1", InputHash: Hash([]byte(in.SerializedInput)), BindingHash: Hash(canonical(in)), Coverage: "unknown"}
	if in.ModelID == "" || in.TokenizerVersion == "" || in.TemplateVersion == "" || in.AdapterVersion == "" || len(in.ModelID) > 256 || len(in.TokenizerVersion) > 256 || len(in.TemplateVersion) > 256 || len(in.AdapterVersion) > 256 || len(in.SerializedInput) > 1<<20 || in.Coverage != "serialized_prompt" && in.Coverage != "complete_model_request" {
		return out, invalid("count binding")
	}
	if counter == nil {
		return out, nil
	}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	count, e := counter(ctx, in)
	if e != nil {
		return out, e
	}
	if e = ctx.Err(); e != nil {
		return out, e
	}
	if count < 0 {
		return out, invalid("negative count")
	}
	out.Count = &count
	out.Coverage = in.Coverage
	return out, nil
}
func (r InputCountReceipt) Matches(in InputCountRequest) bool {
	return r.Version == "iu-input-count-v1" && r.Count != nil && *r.Count >= 0 && r.InputHash == Hash([]byte(in.SerializedInput)) && r.BindingHash == Hash(canonical(in)) && r.Coverage == in.Coverage
}

// Only a contract-tested Adapter that supplies the COMPLETE actual request can
// opt into this helper. No current production CLI is enabled by this contract.
func EnforceInputTokenLimit(r InputCountReceipt, in InputCountRequest, limit int64, adapterContractHash string) error {
	if !job.ValidHash(adapterContractHash) || in.Coverage != "complete_model_request" || !r.Matches(in) || limit < 1 {
		return fmt.Errorf("CONTEXT_TOKEN_BUDGET_UNSUPPORTED")
	}
	if *r.Count > limit {
		return fmt.Errorf("CONTEXT_INSUFFICIENT: input tokens exceed limit")
	}
	return nil
}
func requiredEvidenceCovered(required Span, selected []Span) bool {
	spans := []Span{}
	for _, s := range selected {
		if s.Source == required.Source && s.SHA256 == required.SHA256 {
			spans = append(spans, s)
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	end := required.Start
	for _, s := range spans {
		if s.End <= end {
			continue
		}
		if s.Start > end {
			return false
		}
		end = s.End
		if end >= required.End {
			return true
		}
	}
	return end >= required.End
}
func (m ContextManifest) ValidateInput(model, text string) error {
	fingerprint := Hash(canonical(struct {
		Body  json.RawMessage
		Model string
	}{json.RawMessage(text), model}))
	if m.Version != "iu-context-v1" || m.Model != model || m.Text != text || m.SHA256 != Hash([]byte(text)) || m.InputFingerprint != fingerprint {
		return fmt.Errorf("IU_CONTEXT_MISMATCH")
	}
	return nil
}
