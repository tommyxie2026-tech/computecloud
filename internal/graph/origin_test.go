package graph

import (
	"encoding/json"
	"testing"
)

func originIdentity() ImportIdentity {
	return ImportIdentity{GoalID: "goal_source", Generation: 1, JobID: "job_source", NodeID: "extract", ContextHash: Hash([]byte("context"))}
}
func TestImportedOriginTravelsInsideModelContext(t *testing.T) {
	imported, e := ImportView(importFixture(), []string{"raw", "total"}, "target", originIdentity())
	if e != nil {
		t.Fatal(e)
	}
	if imported.View.Version != "iu-memory-view-v2" {
		t.Fatal("new origin wire did not receive a version")
	}
	for _, r := range imported.View.Records {
		if r.Origin == nil || r.Origin.Source.JobID != "job_source" || r.Origin.ViewHash != imported.OriginViewHash || r.Origin.RecordID == "" {
			t.Fatal("origin not carried", r)
		}
	}
	m, e := CompileContext(ContextRequest{Instructions: "use original identities", Model: "m", View: imported.View})
	if e != nil {
		t.Fatal(e)
	}
	var body struct{ Memory MemoryView }
	if e = json.Unmarshal([]byte(m.Text), &body); e != nil {
		t.Fatal(e)
	}
	if body.Memory.Records[0].Origin == nil {
		t.Fatal("worker input omitted the identity chain")
	}
	second := originIdentity()
	second.Generation = 2
	second.JobID = "job_second"
	next, e := ImportView(imported.View, []string{imported.IDs["raw"], imported.IDs["total"]}, "third", second)
	if e != nil {
		t.Fatal(e)
	}
	if next.View.Records[0].Origin.Previous == nil || next.View.Records[0].Origin.Previous.Source.Generation != 1 {
		t.Fatal("origin chain lost")
	}
}
func TestModelCannotMintImportedOrigin(t *testing.T) {
	r := importFixture().Records[0]
	r.Validation = "unchecked"
	r.Origin = &MemoryOrigin{Version: "iu-record-origin-v1", Source: originIdentity(), ViewHash: EmptyView().SHA256, RecordID: "raw", RecordHash: Hash([]byte("r"))}
	raw := canonical(Delta{Version: "iu-memory-delta-v1", ReadViewHash: EmptyView().SHA256, ContextHash: Hash([]byte("ctx")), Records: []MemoryRecord{r}})
	if _, e := ValidateDelta(raw, EmptyView()); e == nil {
		t.Fatal("model forged origin")
	}
}
