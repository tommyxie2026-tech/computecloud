package aicloudadapter

import (
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"sort"
)

// Config/digest normalization mirrors the audited aicloud evaluation contract,
// not a local evaluation policy. Future upstream changes must fail the digest check.
type EvaluationConfig struct {
	ModelID            string            `json:"modelId"`
	ModelVersion       string            `json:"modelVersion"`
	Provider           string            `json:"provider"`
	EndpointID         string            `json:"endpointId,omitempty"`
	PromptVersion      string            `json:"promptVersion"`
	WorkflowVersion    string            `json:"workflowVersion"`
	ToolVersions       map[string]string `json:"toolVersions,omitempty"`
	PermissionVersion  string            `json:"permissionVersion,omitempty"`
	TokenBudget        int               `json:"tokenBudget"`
	TimeBudgetMS       int64             `json:"timeBudgetMs"`
	RetryPolicyVersion string            `json:"retryPolicyVersion"`
	CompactionVersion  string            `json:"compactionVersion,omitempty"`
	SandboxProfile     string            `json:"sandboxProfile"`
	DatasetID          string            `json:"datasetId"`
	DatasetVersion     string            `json:"datasetVersion"`
	EvaluatorID        string            `json:"evaluatorId"`
	EvaluatorVersion   string            `json:"evaluatorVersion"`
	Parameters         map[string]string `json:"parameters,omitempty"`
}
type configKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func sortedConfig(values map[string]string) []configKV {
	keys := []string{}
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []configKV
	for _, k := range keys {
		out = append(out, configKV{k, values[k]})
	}
	return out
}
func EvaluationConfigDigest(c EvaluationConfig) (string, error) {
	for _, v := range []string{c.ModelID, c.ModelVersion, c.Provider, c.PromptVersion, c.WorkflowVersion, c.RetryPolicyVersion, c.SandboxProfile, c.DatasetID, c.DatasetVersion, c.EvaluatorID, c.EvaluatorVersion} {
		if v == "" {
			return "", fmt.Errorf("AICLOUD_EVALUATION_CONFIG_INCOMPLETE")
		}
	}
	if c.TokenBudget <= 0 || c.TimeBudgetMS <= 0 {
		return "", fmt.Errorf("AICLOUD_EVALUATION_BUDGET_INCOMPLETE")
	}
	canonical := struct {
		ModelID            string     `json:"modelId"`
		ModelVersion       string     `json:"modelVersion"`
		Provider           string     `json:"provider"`
		EndpointID         string     `json:"endpointId,omitempty"`
		PromptVersion      string     `json:"promptVersion"`
		WorkflowVersion    string     `json:"workflowVersion"`
		ToolVersions       []configKV `json:"toolVersions,omitempty"`
		PermissionVersion  string     `json:"permissionVersion,omitempty"`
		TokenBudget        int        `json:"tokenBudget"`
		TimeBudgetMS       int64      `json:"timeBudgetMs"`
		RetryPolicyVersion string     `json:"retryPolicyVersion"`
		CompactionVersion  string     `json:"compactionVersion,omitempty"`
		SandboxProfile     string     `json:"sandboxProfile"`
		DatasetID          string     `json:"datasetId"`
		DatasetVersion     string     `json:"datasetVersion"`
		EvaluatorID        string     `json:"evaluatorId"`
		EvaluatorVersion   string     `json:"evaluatorVersion"`
		Parameters         []configKV `json:"parameters,omitempty"`
	}{c.ModelID, c.ModelVersion, c.Provider, c.EndpointID, c.PromptVersion, c.WorkflowVersion, sortedConfig(c.ToolVersions), c.PermissionVersion, c.TokenBudget, c.TimeBudgetMS, c.RetryPolicyVersion, c.CompactionVersion, c.SandboxProfile, c.DatasetID, c.DatasetVersion, c.EvaluatorID, c.EvaluatorVersion, sortedConfig(c.Parameters)}
	return "sha256:" + job.Hash(job.JSON(canonical)), nil
}
