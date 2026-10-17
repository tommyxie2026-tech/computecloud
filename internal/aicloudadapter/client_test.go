package aicloudadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyAICloudBundleFreezesExactVersions(t *testing.T) {
	now := time.Now().UTC()
	config := EvaluationConfig{ModelID: "model", ModelVersion: "1", Provider: "fixture", PromptVersion: "p1", WorkflowVersion: "w1", TokenBudget: 100, TimeBudgetMS: 1000, RetryPolicyVersion: "r1", SandboxProfile: "read-only", DatasetID: "orders", DatasetVersion: "1", EvaluatorID: "quality", EvaluatorVersion: "1"}
	configDigest, e := EvaluationConfigDigest(config)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("mutating remote request")
		}
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing scoped token")
		}
		var body any
		switch r.URL.Path {
		case "/api/v1/models/model":
			body = map[string]any{"id": "model", "version": "1", "lifecycle": "active", "artifactDigest": "sha256:" + strings.Repeat("a", 64), "evaluationVersion": "e1", "approvalStatus": "approved", "health": "healthy"}
		case "/api/v1/tasks/task/routes":
			body = map[string]any{"items": []any{map[string]any{"id": "route", "taskId": "task", "selected": map[string]any{"modelId": "model", "modelVersion": "1", "deploymentId": "deployment"}, "policyVersion": "p1", "evidenceVersion": "e1", "createdAt": now}}}
		case "/api/v1/tasks/task/evaluations":
			body = map[string]any{"items": []any{map[string]any{"id": "eval", "taskId": "task", "configDigest": configDigest, "config": config, "gate": map[string]any{"passed": true}}}}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL + "/api/v1", Token: "fixture"}
	request := Request{ModelArtifactDigest: "sha256:" + strings.Repeat("a", 64), DeploymentID: "deployment", EvaluationConfigDigest: configDigest, TaskID: "task", ModelID: "model", ModelVersion: "1", RouteID: "route", EvaluationID: "eval", PolicyVersion: "p1", EvidenceVersion: "e1", MaxAge: time.Minute}
	bundle, e := c.Fetch(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	frozen, e := Freeze(bundle, request, now)
	if e != nil {
		t.Fatal(e)
	}
	if frozen.ModelVersion != "1" || frozen.RouteID != "route" || frozen.EvaluationConfigHash != strings.TrimPrefix(configDigest, "sha256:") || frozen.DeploymentID != "deployment" {
		t.Fatal("lost binding")
	}
	request.ModelVersion = "2"
	if _, e = Freeze(bundle, request, now); e == nil {
		t.Fatal("model silently switched")
	}
}
func TestAICloudUnavailabilityRateLimitBudgetAndRedirectNeverFallback(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusPaymentRequired, http.StatusFound} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.invalid/secret")
				w.WriteHeader(code)
			}))
			defer server.Close()
			c := Client{BaseURL: server.URL + "/api/v1", Token: "fixture"}
			if _, e := c.Fetch(context.Background(), Request{TaskID: "task", ModelID: "model", RouteID: "route", EvaluationID: "eval"}); e == nil {
				t.Fatal("unavailable decision fell back")
			}
		})
	}
}
func TestAICloudPaginationLoopAndMissingDecisionFailClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "nextPageToken": "same"})
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL + "/api/v1", Token: "fixture"}
	if _, e := c.find(context.Background(), "/tasks/task/routes", "missing"); e == nil {
		t.Fatal("pagination loop accepted")
	}
}
