package iuexperiment

import "testing"

func TestIncrementalEvidenceKeepsUnknownCostAndRequiresProvenance(t *testing.T) {
	full := IncrementalArm{DatasetHash: "same", OutputHash: "same", BusinessVerdict: "PASS", Tasks: 4, Attempts: 4}
	incremental := full
	incremental.Tasks = 2
	incremental.Attempts = 2
	incremental.ReusedNodes = 2
	if _, e := CompareIncremental(full, incremental); e == nil {
		t.Fatal("missing provenance")
	}
	incremental.Provenance = []string{"job/a", "job/b"}
	r, e := CompareIncremental(full, incremental)
	if e != nil || !r.Equivalent || !r.FewerTasks || r.Incremental.CostUSD != nil || r.Incremental.ProviderCalls != nil {
		t.Fatal("invented measurement", e)
	}
	incremental.BusinessVerdict = "UNKNOWN"
	r, e = CompareIncremental(full, incremental)
	if e != nil || r.Equivalent {
		t.Fatal("unknown called equivalent")
	}
}
