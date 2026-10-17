package graph

import (
	"context"
	"strings"
	"testing"
)

func testCounter(ctx context.Context, in InputCountRequest) (int64, error) {
	return int64(len([]byte(in.SerializedInput))), nil
} // fixture byte tokenizer only
func TestTokenizerChangeRequiresRecount(t *testing.T) {
	in := InputCountRequest{ModelID: "fixture-byte-lm", TokenizerVersion: "fixture-bytes-1", TemplateVersion: "1", AdapterVersion: "fixture-stdin-1", Coverage: "serialized_prompt", SerializedInput: "你好🙂"}
	r, e := CountInput(context.Background(), in, testCounter)
	if e != nil {
		t.Fatal(e)
	}
	if r.Count == nil || !r.Matches(in) {
		t.Fatal("missing count")
	}
	in.TokenizerVersion = "2"
	if r.Matches(in) {
		t.Fatal("old count reused")
	}
	in.TokenizerVersion = "fixture-bytes-1"
	in.AdapterVersion = "fixture-stdin-2"
	if r.Matches(in) {
		t.Fatal("wrapper drift reused")
	}
}
func TestModelSwitchDoesNotReuseTokenIDs(t *testing.T) {
	in := InputCountRequest{ModelID: "a", TokenizerVersion: "1", TemplateVersion: "1", AdapterVersion: "1", Coverage: "serialized_prompt", SerializedInput: "你好🙂"}
	r, e := CountInput(context.Background(), in, testCounter)
	if e != nil {
		t.Fatal(e)
	}
	in.ModelID = "b"
	if r.Matches(in) {
		t.Fatal("count reused across model")
	}
	r, e = CountInput(context.Background(), in, nil)
	if e != nil || r.Count != nil || r.Coverage != "unknown" {
		t.Fatal("counter unavailable became zero")
	}
}
func TestRequiredContextCannotBeTruncated(t *testing.T) {
	r := ContextRequest{Instructions: strings.Repeat("规", 500), Model: "m", View: EmptyView(), Policy: &ContextPolicy{Version: "iu-context-policy-v1", MaxBytes: 1024}}
	if _, e := CompileContext(r); e == nil {
		t.Fatal("required context truncated")
	}
}
func TestCrossChunkReferenceNeedsEvidence(t *testing.T) {
	src := Source{ID: "whole", Text: "甲方指代前文，乙方付款🙂"}
	src.SHA256 = Hash([]byte(src.Text))
	a := Span{Source: src.ID, Start: 0, End: 6, SHA256: src.SHA256}
	b := Span{Source: src.ID, Start: 6, End: len(src.Text), SHA256: src.SHA256}
	r := ContextRequest{Instructions: "resolve reference", Model: "m", View: EmptyView(), Sources: []Source{src}, Spans: []Span{b}, Policy: &ContextPolicy{Version: "iu-context-policy-v1", RequiredEvidence: []Span{a}}}
	if _, e := CompileContext(r); e == nil {
		t.Fatal("missing reference evidence accepted")
	}
	r.Spans = append(r.Spans, a)
	if _, e := CompileContext(r); e != nil {
		t.Fatal(e)
	}
}

func TestCountUnavailableAndWrapperCoverageStayUnknown(t *testing.T) {
	in := InputCountRequest{ModelID: "fixture-byte-lm", TokenizerVersion: "fixture-bytes-1", TemplateVersion: "1", AdapterVersion: "fixture-stdin-1", Coverage: "serialized_prompt", SerializedInput: "hello"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := CountInput(ctx, in, testCounter)
	if e == nil || r.Count != nil || r.Coverage != "unknown" {
		t.Fatal("cancelled counter produced count")
	}
	r, e = CountInput(context.Background(), in, testCounter)
	if e != nil {
		t.Fatal(e)
	}
	if e = EnforceInputTokenLimit(r, in, 10, Hash([]byte("contract"))); e == nil {
		t.Fatal("partial CLI wrapper enforced full budget")
	}
	in.Coverage = "complete_model_request"
	r, e = CountInput(context.Background(), in, testCounter)
	if e != nil {
		t.Fatal(e)
	}
	if e = EnforceInputTokenLimit(r, in, 4, Hash([]byte("fixture byte tokenizer contract"))); e == nil {
		t.Fatal("token budget exceeded")
	}
	in.SerializedInput += "changed"
	if r.Matches(in) {
		t.Fatal("input hash drift reused count")
	}
}
func TestContextPolicyV4RejectsUnsupportedTokensAndAttachments(t *testing.T) {
	s := fixture()
	s.Version = ContextVersion
	s.Nodes[0].ContextPolicy = &ContextPolicy{Version: "iu-context-policy-v1", MaxBytes: 2048}
	p, e := Compile(s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Decode(canonical(p.Spec)); e != nil {
		t.Fatal(e)
	}
	limit := int64(1)
	s.Nodes[0].ContextPolicy.MaxInputTokens = &limit
	if _, e = Compile(s); e == nil {
		t.Fatal("unsupported real tokenizer budget accepted")
	}
	s.Nodes[0].ContextPolicy.MaxInputTokens = nil
	s.Version = ReuseVersion
	if _, e = Compile(s); e == nil {
		t.Fatal("v3 accepted new policy")
	}
	raw := strings.Replace(string(canonical(p.Spec)), `"sources":`, `"attachments":[{"media_type":"application/octet-stream"}],"sources":`, 1)
	if _, e = Decode([]byte(raw)); e == nil {
		t.Fatal("unsupported attachment reinterpreted as text")
	}
}
