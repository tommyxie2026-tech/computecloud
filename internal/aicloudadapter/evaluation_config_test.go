package aicloudadapter

import "testing"

func TestAICloudEvaluationDigestMatchesAuditedCanonicalFormat(t *testing.T) {
	c := EvaluationConfig{ModelID: "model", ModelVersion: "1", Provider: "fixture", PromptVersion: "p1", WorkflowVersion: "w1", TokenBudget: 100, TimeBudgetMS: 1000, RetryPolicyVersion: "r1", SandboxProfile: "read-only", DatasetID: "orders", DatasetVersion: "1", EvaluatorID: "quality", EvaluatorVersion: "1"}
	digest, e := EvaluationConfigDigest(c)
	if e != nil || digest != "sha256:3bbfa85e5fa630ce8161d306f3be46aadab5f83e9b8af0e914e4c03f66bee601" {
		t.Fatal(digest, e)
	}
	c.ToolVersions = map[string]string{"z": "1", "a": "2"}
	a, e := EvaluationConfigDigest(c)
	if e != nil {
		t.Fatal(e)
	}
	c.ToolVersions = map[string]string{"a": "2", "z": "1"}
	b, e := EvaluationConfigDigest(c)
	if e != nil || a != b {
		t.Fatal("map order changed digest")
	}
	c.TokenBudget = 0
	if _, e = EvaluationConfigDigest(c); e == nil {
		t.Fatal("missing evaluation budget accepted")
	}
}
