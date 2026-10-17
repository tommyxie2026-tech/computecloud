package graph

import (
	"strings"
	"testing"
)

func TestCompileContextPreservesSourceAndRequiredRules(t *testing.T) {
	src := Source{ID: "source", Text: "规则：满一万减一千。订单甲：6000。"}
	src.SHA256 = Hash([]byte(src.Text))
	req := ContextRequest{Sources: []Source{src}, Rules: []Span{{Source: src.ID, Start: 0, End: 30, SHA256: src.SHA256}}, Spans: []Span{{Source: src.ID, Start: 30, End: len(src.Text), SHA256: src.SHA256}}, Instructions: "提取订单", View: EmptyView(), Model: "small"}
	m, e := CompileContext(req)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(m.Text, "满一万减一千") || !strings.Contains(m.Text, "订单甲") || m.SHA256 != Hash([]byte(m.Text)) || m.TokenCount != nil || m.TokenCoverage != "unknown" {
		t.Fatalf("manifest: %+v", m)
	}
	if req.Sources[0].Text != src.Text {
		t.Fatal("mutated source")
	}
	req.Rules[0].SHA256 = strings.Repeat("0", 64)
	if _, e = CompileContext(req); e == nil {
		t.Fatal("missing rule source accepted")
	}
}

func TestContextNeverSilentlyTruncates(t *testing.T) {
	if _, e := CompileContext(ContextRequest{Instructions: strings.Repeat("x", 65536), View: EmptyView(), Model: "small"}); e == nil || !strings.Contains(e.Error(), "CONTEXT_INSUFFICIENT") {
		t.Fatalf("expected context limit, got %v", e)
	}
	src := Source{ID: "s", Text: "中文"}
	src.SHA256 = Hash([]byte(src.Text))
	if _, e := CompileContext(ContextRequest{Sources: []Source{src}, Spans: []Span{{Source: "s", Start: 1, End: 3, SHA256: src.SHA256}}, View: EmptyView(), Model: "small"}); e == nil {
		t.Fatal("split UTF-8 accepted")
	}
}

func TestReconstructSourceValidatesOverlapsAndCoverage(t *testing.T) {
	src := Source{ID: "s", Text: "甲乙丙"}
	src.SHA256 = Hash([]byte(src.Text))
	spans := []Span{{Source: "s", Start: 0, End: 6, SHA256: src.SHA256}, {Source: "s", Start: 3, End: 9, SHA256: src.SHA256}}
	text, e := ReconstructSource(src, spans)
	if e != nil || text != "甲乙丙" {
		t.Fatalf("reconstruct %q %v", text, e)
	}
	if _, e = ReconstructSource(src, spans[:1]); e == nil {
		t.Fatal("coverage gap accepted")
	}
}
