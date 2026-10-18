package graph

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func coverageFixture() Spec {
	s := fixture()
	s.Version = CoverageVersion
	src := s.Sources[0]
	src.SHA256 = Hash([]byte(src.Text))
	s.Sources[0] = src
	sp := Span{Source: src.ID, End: len(src.Text), SHA256: src.SHA256}
	s.Nodes[0].Spans = []Span{sp}
	s.Coverage = &CoverageCatalog{Version: "iu-coverage-v1", GoalSourceID: src.ID, GoalSpecHash: src.SHA256, Profile: "orders", ProfileVersion: "1", Scope: "orders", Obligations: []CoverageObligation{{ID: "identity", Sources: []Span{sp}, NodeIDs: []string{"b"}, CheckID: "source_identity"}}, Checks: []CoverageCheckBinding{{ID: "source_identity", Outputs: []Output{s.Outputs[0]}}}}
	s.Coverage.SHA256 = s.Coverage.Digest()
	return s
}
func TestCoveragePreflightReportsExactGap(t *testing.T) {
	s := coverageFixture()
	s.Nodes[0].Spans = nil
	r, e := CheckCoverage(s)
	if e != nil || r.Complete || len(r.Gaps) != 1 || r.Gaps[0].NodeID != "b" || r.Gaps[0].ConstraintID != "identity" {
		t.Fatalf("%+v %v", r, e)
	}
	_, e = Compile(s)
	var ce *CoverageError
	if !errors.As(e, &ce) {
		t.Fatalf("missing structured gap: %v", e)
	}
}
func TestCoverageV5RejectsMissingReferencesAndSpans(t *testing.T) {
	for _, change := range []func(*Spec){func(s *Spec) { s.Coverage = nil }, func(s *Spec) { s.Coverage.Obligations[0].NodeIDs = []string{"missing"} }, func(s *Spec) { s.Coverage.Checks[0].Outputs[0].Node = "missing" }, func(s *Spec) { s.Coverage.Obligations[0].Sources[0].End = 1 }, func(s *Spec) { s.Coverage.Obligations = append(s.Coverage.Obligations, s.Coverage.Obligations[0]) }, func(s *Spec) { s.Coverage.GoalSpecHash = Hash([]byte("wrong")) }} {
		s := coverageFixture()
		change(&s)
		if s.Coverage != nil {
			s.Coverage.SHA256 = s.Coverage.Digest()
		}
		if _, e := Compile(s); e == nil {
			t.Fatal("invalid coverage accepted")
		}
	}
	s := coverageFixture()
	p, e := Compile(s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Decode(canonical(p.Spec)); e != nil {
		t.Fatal(e)
	}
	s.Version = ContextVersion
	if _, e = Compile(s); e == nil {
		t.Fatal("legacy coverage accepted")
	}
}
func TestCoverageVersionPreservesLegacyPlanHashes(t *testing.T) {
	want := map[string]string{Version: "c5a194834398fc0e55670c2c1c40548ae83315afd06cb3ee2893378183e3f57d", ImportVersion: "9290758303d4c4e3a5b8dd2abfea3bd32ca237dbcb11f876b032d8d7dbb22136", ReuseVersion: "2287826403d7f96bbaf413103c54fae6d105f1e05368688f4d8279d6c15f921b", ContextVersion: "e7475b619a92ba1ab7edc82124ed603147c5fcd7e57e571394270f77ac6436cb"}
	for v, h := range want {
		s := fixture()
		s.Version = v
		p, e := Compile(s)
		if e != nil || p.Digest != h {
			t.Fatalf("%s %s %v", v, p.Digest, e)
		}
	}
}
func TestCoverageContextPolicyDeduplicatesAndRejectsOverflow(t *testing.T) {
	s := coverageFixture()
	for i := 0; i < 10; i++ {
		o := s.Coverage.Obligations[0]
		o.ID = fmt.Sprintf("c%d", i)
		s.Coverage.Obligations = append(s.Coverage.Obligations, o)
	}
	p, e := CoverageContextPolicy(s, s.Nodes[0])
	if e != nil || len(p.RequiredEvidence) != 1 {
		t.Fatal("duplicate obligations inflated input policy", p, e)
	}
	text := strings.Repeat("a", 200)
	s.Sources[0].Text = text
	s.Sources[0].SHA256 = Hash([]byte(text))
	s.Coverage.GoalSpecHash = s.Sources[0].SHA256
	s.Coverage.Obligations = nil
	all := Span{Source: s.Sources[0].ID, End: 200, SHA256: s.Sources[0].SHA256}
	s.Nodes[0].Spans = []Span{all}
	for i := 0; i < 5; i++ {
		o := CoverageObligation{ID: fmt.Sprintf("batch%d", i), NodeIDs: []string{"b"}, CheckID: "source_identity"}
		for j := i * 32; j < (i+1)*32 && j < 129; j++ {
			o.Sources = append(o.Sources, Span{Source: all.Source, Start: j, End: j + 1, SHA256: all.SHA256})
		}
		s.Coverage.Obligations = append(s.Coverage.Obligations, o)
	}
	s.Coverage.SHA256 = s.Coverage.Digest()
	if _, e = Compile(s); e == nil {
		t.Fatal("unusable runtime policy was published")
	}
}

func TestCoverageRejectsJoinEvidenceResponsibility(t *testing.T) {
	s := coverageFixture()
	s.Coverage.Obligations[0].NodeIDs = []string{"merge"}
	s.Nodes[1].Spans = append([]Span{}, s.Coverage.Obligations[0].Sources...)
	s.Coverage.SHA256 = s.Coverage.Digest()
	if _, err := Compile(s); err == nil {
		t.Fatal("join declared evidence it never loads into an execution context")
	}
}
