// Package aicloudadapter reads existing aicloud evidence. It is not a Router,
// Registry, authorization service or a computecloud production routing switch.
package aicloudadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const AuditedSourceCommit = "97506084408eb9e77c60dcaeea059580469be2f4"
const AuditedOpenAPIHash = "f80ac1ae9054ad867dc9871c00e1dff579fd83d144262c1e3f9c0124465b2ce9"

type Client struct {
	BaseURL string
	Token   string
}
type Request struct {
	ModelArtifactDigest    string        `json:"model_artifact_digest"`
	DeploymentID           string        `json:"deployment_id"`
	EvaluationConfigDigest string        `json:"evaluation_config_digest"`
	TaskID                 string        `json:"task_id"`
	ModelID                string        `json:"model_id"`
	ModelVersion           string        `json:"model_version"`
	RouteID                string        `json:"route_id"`
	EvaluationID           string        `json:"evaluation_id"`
	PolicyVersion          string        `json:"policy_version"`
	EvidenceVersion        string        `json:"evidence_version"`
	MaxAge                 time.Duration `json:"max_age_ns"`
}
type Bundle struct{ Model, Route, Evaluation json.RawMessage }
type FrozenBinding struct {
	Version              string    `json:"version"`
	SourceCommit         string    `json:"audited_source_commit"`
	ContractHash         string    `json:"audited_openapi_sha256"`
	TaskID               string    `json:"source_task_id"`
	ModelID              string    `json:"model_id"`
	ModelVersion         string    `json:"model_version"`
	ModelArtifactDigest  string    `json:"model_artifact_digest"`
	DeploymentID         string    `json:"deployment_id"`
	RouteID              string    `json:"route_id"`
	PolicyVersion        string    `json:"policy_version"`
	EvidenceVersion      string    `json:"evidence_version"`
	EvaluationID         string    `json:"evaluation_id"`
	EvaluationConfigHash string    `json:"evaluation_config_sha256"`
	EvaluatorID          string    `json:"evaluator_id"`
	EvaluatorVersion     string    `json:"evaluator_version"`
	DatasetID            string    `json:"dataset_id"`
	DatasetVersion       string    `json:"dataset_version"`
	ModelHash            string    `json:"registry_response_sha256"`
	RouteHash            string    `json:"route_response_sha256"`
	EvaluationHash       string    `json:"evaluation_response_sha256"`
	ExpiresAt            time.Time `json:"expires_at"`
	SHA256               string    `json:"sha256"`
}

func (c Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	base, e := url.Parse(c.BaseURL)
	if e != nil || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Host == "" || strings.TrimRight(base.Path, "/") != "/api/v1" || c.Token == "" || strings.ContainsAny(c.Token, "\r\n") {
		return nil, fmt.Errorf("AICLOUD_CONFIG_INVALID")
	}
	ip := net.ParseIP(base.Hostname())
	if base.Scheme != "https" && !(base.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, fmt.Errorf("AICLOUD_TLS_REQUIRED")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawQuery = query.Encode()
	req, e := http.NewRequestWithContext(ctx, "GET", base.String(), nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("AICLOUD_UNAVAILABLE")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("AICLOUD_HTTP_%d", res.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return nil, fmt.Errorf("AICLOUD_RESPONSE_LIMIT")
	}
	if e = jsonutil.Validate(raw); e != nil {
		return nil, fmt.Errorf("AICLOUD_RESPONSE_INVALID")
	}
	return raw, nil
}
func (c Client) find(ctx context.Context, path, id string) (json.RawMessage, error) {
	token := ""
	seen := map[string]bool{}
	var found json.RawMessage
	for page := 0; page < 4; page++ {
		raw, e := c.get(ctx, path, url.Values{"pageSize": {"32"}, "pageToken": {token}})
		if e != nil {
			return nil, e
		}
		var list struct {
			Items []json.RawMessage `json:"items"`
			Next  string            `json:"nextPageToken"`
		}
		if e = json.Unmarshal(raw, &list); e != nil || len(list.Items) > 32 || len(list.Next) > 1024 {
			return nil, fmt.Errorf("AICLOUD_LIST_INVALID")
		}
		for _, item := range list.Items {
			var head struct {
				ID string `json:"id"`
			}
			if e = json.Unmarshal(item, &head); e != nil {
				return nil, e
			}
			if head.ID == id {
				if found != nil {
					return nil, fmt.Errorf("AICLOUD_DUPLICATE_ID")
				}
				found = append(json.RawMessage{}, item...)
			}
		}
		if list.Next == "" {
			if found == nil {
				return nil, fmt.Errorf("AICLOUD_DECISION_MISSING")
			}
			return found, nil
		}
		if seen[list.Next] {
			return nil, fmt.Errorf("AICLOUD_PAGINATION_LOOP")
		}
		seen[list.Next] = true
		token = list.Next
	}
	return nil, fmt.Errorf("AICLOUD_PAGINATION_LIMIT")
}
func (c Client) Fetch(ctx context.Context, r Request) (Bundle, error) {
	var b Bundle
	for _, id := range []string{r.TaskID, r.ModelID, r.RouteID, r.EvaluationID} {
		if !job.Ref(id) {
			return b, fmt.Errorf("AICLOUD_REFERENCE_INVALID")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var e error
	b.Model, e = c.get(ctx, "/models/"+url.PathEscape(r.ModelID), nil)
	if e != nil {
		return Bundle{}, e
	}
	b.Route, e = c.find(ctx, "/tasks/"+url.PathEscape(r.TaskID)+"/routes", r.RouteID)
	if e != nil {
		return Bundle{}, e
	}
	b.Evaluation, e = c.find(ctx, "/tasks/"+url.PathEscape(r.TaskID)+"/evaluations", r.EvaluationID)
	if e != nil {
		return Bundle{}, e
	}
	return b, nil
}
func Freeze(b Bundle, r Request, now time.Time) (FrozenBinding, error) {
	var out FrozenBinding
	var model struct{ ID, Version, Lifecycle, ArtifactDigest, EvaluationVersion, ApprovalStatus, Health string }
	var route struct {
		ID, TaskID, PolicyVersion, EvidenceVersion string
		Selected                                   struct{ ModelID, ModelVersion, DeploymentID string }
		CreatedAt                                  time.Time
	}
	var evaluation struct {
		ID, TaskID, ConfigDigest string
		Config                   EvaluationConfig
		Gate                     struct{ Passed bool }
	}

	for _, raw := range []json.RawMessage{b.Model, b.Route, b.Evaluation} {
		if len(raw) > 1<<20 || jsonutil.Validate(raw) != nil {
			return out, fmt.Errorf("AICLOUD_EVIDENCE_INVALID")
		}
	}
	if json.Unmarshal(b.Model, &model) != nil || json.Unmarshal(b.Route, &route) != nil || json.Unmarshal(b.Evaluation, &evaluation) != nil {
		return out, fmt.Errorf("AICLOUD_EVIDENCE_INVALID")
	}
	if model.ID != r.ModelID || model.Version != r.ModelVersion || model.Lifecycle != "active" || model.ApprovalStatus != "approved" || model.Health != "healthy" || model.EvaluationVersion != r.EvidenceVersion || r.ModelVersion == "" || r.EvidenceVersion == "" || r.PolicyVersion == "" {
		return out, fmt.Errorf("AICLOUD_REGISTRY_DRIFT_OR_RETIRED")
	}
	digest := strings.TrimPrefix(model.ArtifactDigest, "sha256:")
	if !strings.HasPrefix(model.ArtifactDigest, "sha256:") || !job.ValidHash(digest) || model.ArtifactDigest != r.ModelArtifactDigest {
		return out, fmt.Errorf("AICLOUD_MODEL_ARTIFACT_UNPINNED")
	}
	if route.ID != r.RouteID || route.TaskID != r.TaskID || route.PolicyVersion != r.PolicyVersion || route.EvidenceVersion != r.EvidenceVersion || route.Selected.ModelID != r.ModelID || route.Selected.ModelVersion != r.ModelVersion || route.Selected.DeploymentID == "" || route.Selected.DeploymentID != r.DeploymentID || r.MaxAge <= 0 || r.MaxAge > 15*time.Minute || route.CreatedAt.After(now) || !route.CreatedAt.Add(r.MaxAge).After(now) {
		return out, fmt.Errorf("AICLOUD_ROUTE_DRIFT_OR_STALE")
	}
	configDigest, configErr := EvaluationConfigDigest(evaluation.Config)
	if evaluation.ID != r.EvaluationID || evaluation.TaskID != r.TaskID || evaluation.Config.ModelID != r.ModelID || evaluation.Config.ModelVersion != r.ModelVersion || !evaluation.Gate.Passed || (configErr != nil || configDigest != evaluation.ConfigDigest || configDigest != r.EvaluationConfigDigest) || evaluation.Config.EvaluatorID == "" || evaluation.Config.EvaluatorVersion == "" || evaluation.Config.DatasetID == "" || evaluation.Config.DatasetVersion == "" {
		return out, fmt.Errorf("AICLOUD_EVALUATION_UNPROVEN")
	}
	out = FrozenBinding{Version: "iu-aicloud-binding-v1", SourceCommit: AuditedSourceCommit, ContractHash: AuditedOpenAPIHash, TaskID: r.TaskID, ModelID: r.ModelID, ModelVersion: r.ModelVersion, ModelArtifactDigest: model.ArtifactDigest, DeploymentID: route.Selected.DeploymentID, RouteID: r.RouteID, PolicyVersion: r.PolicyVersion, EvidenceVersion: r.EvidenceVersion, EvaluationID: r.EvaluationID, EvaluationConfigHash: strings.TrimPrefix(evaluation.ConfigDigest, "sha256:"), EvaluatorID: evaluation.Config.EvaluatorID, EvaluatorVersion: evaluation.Config.EvaluatorVersion, DatasetID: evaluation.Config.DatasetID, DatasetVersion: evaluation.Config.DatasetVersion, ModelHash: job.Hash(b.Model), RouteHash: job.Hash(b.Route), EvaluationHash: job.Hash(b.Evaluation), ExpiresAt: route.CreatedAt.Add(r.MaxAge)}
	out.SHA256 = job.Hash(job.JSON(out))
	return out, nil
}

// SameBinding compares immutable content; a digest is never an authorization.
func SameBinding(a, b FrozenBinding) bool {
	return a.SHA256 != "" && bytes.Equal(job.JSON(a), job.JSON(b))
}
