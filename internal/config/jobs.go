package config

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

type HTTP struct {
	Listen         string   `yaml:"listen"`
	AllowedOrigins []string `yaml:"allowed_origins"`
}
type MCP struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}
type JobTemplate struct {
	RuntimeProfile    string `yaml:"runtime_profile" json:"runtime_profile"`
	PolicyRef         string `yaml:"policy_ref" json:"policy_ref"`
	AcceptanceProfile string `yaml:"acceptance_profile" json:"acceptance_profile"`
	Digest            string `yaml:"digest" json:"digest"`
}
type Jobs struct {
	Enabled                  bool          `yaml:"enabled"`
	MaxPartitions            int           `yaml:"max_partitions"`
	MaxParallelism           int           `yaml:"max_parallelism"`
	RecommendedParallelism   int           `yaml:"recommended_parallelism"`
	MaxRequestBytes          int64         `yaml:"max_request_bytes"`
	MaxManifestBytes         int64         `yaml:"max_manifest_bytes"`
	MaxReduceInputBytes      int64         `yaml:"max_reduce_input_bytes"`
	MaxAttemptsPerTask       int           `yaml:"max_attempts_per_task"`
	MaxTotalRuntimeSeconds   int64         `yaml:"max_total_runtime_seconds"`
	MaxDeadlineExtendSeconds int64         `yaml:"max_deadline_extend_seconds"`
	MaxTaskEvents            int           `yaml:"max_task_events"`
	SchedulerAgingSeconds    int64         `yaml:"scheduler_aging_seconds"`
	MaxQueuedTasks           int           `yaml:"max_queued_tasks"`
	MaxQueuedTasksPerProject int           `yaml:"max_queued_tasks_per_project"`
	Templates                []JobTemplate `yaml:"templates"`
}
type ConversationJobs struct {
	Enabled         bool                  `yaml:"enabled"`
	LoopbackListen  string                `yaml:"loopback_listen"`
	MaxRequestBytes int64                 `yaml:"max_request_bytes"`
	Profiles        []ConversationProfile `yaml:"profiles"`
}
type ConversationProfile struct {
	ID              string        `yaml:"id"`
	PublicModel     string        `yaml:"public_model"`
	ExecutionOwner  string        `yaml:"execution_owner"`
	ProjectID       string        `yaml:"project_id"`
	Workspace       job.Workspace `yaml:"workspace"`
	Execution       job.Execution `yaml:"execution"`
	Limits          job.Limits    `yaml:"limits"`
	MaxOutputTokens int           `yaml:"max_output_tokens"`
	MaxInputBytes   int           `yaml:"max_input_bytes"`
	MaxActive       int           `yaml:"max_active"`
}
type CodexCLI struct {
	Executable string `yaml:"executable"`
	Version    string `yaml:"version"`
}

type ModelRoute struct {
	Backend       string    `yaml:"backend"`
	CLI           *CodexCLI `yaml:"cli"`
	BaseURL       string    `yaml:"base_url"`
	APIKeyFile    string    `yaml:"api_key_file"`
	AllowedModels []string  `yaml:"allowed_models"`
	MaxInflight   int       `yaml:"max_inflight"`
}
type ModelGateway struct {
	Enabled                  bool                  `yaml:"enabled"`
	PublicBaseURL            string                `yaml:"public_base_url"`
	MaxRequestBytes          int64                 `yaml:"max_request_bytes"`
	MaxInflight              int                   `yaml:"max_inflight"`
	ConnectTimeoutSeconds    int                   `yaml:"connect_timeout_seconds"`
	HeaderTimeoutSeconds     int                   `yaml:"header_timeout_seconds"`
	StreamIdleTimeoutSeconds int                   `yaml:"stream_idle_timeout_seconds"`
	RequestTimeoutSeconds    int                   `yaml:"request_timeout_seconds"`
	Routes                   map[string]ModelRoute `yaml:"routes"`
	CredentialRoutes         map[string]string     `yaml:"credential_routes"`
}

func (c *Server) DefaultV02() {
	j := &c.Jobs
	if j.MaxPartitions == 0 {
		j.MaxPartitions = 32
	}
	if j.MaxParallelism == 0 {
		j.MaxParallelism = 8
	}
	if j.RecommendedParallelism == 0 {
		j.RecommendedParallelism = 2
	}
	if j.MaxRequestBytes == 0 {
		j.MaxRequestBytes = job.MaxRequestBytes
	}
	if j.MaxManifestBytes == 0 {
		j.MaxManifestBytes = job.MaxManifestBytes
	}
	if j.MaxReduceInputBytes == 0 {
		j.MaxReduceInputBytes = job.MaxInputBytes
	}
	if j.MaxAttemptsPerTask == 0 {
		j.MaxAttemptsPerTask = 1
	}
	if j.MaxTotalRuntimeSeconds == 0 {
		j.MaxTotalRuntimeSeconds = 7 * 24 * 60 * 60
	}
	if j.MaxDeadlineExtendSeconds == 0 {
		j.MaxDeadlineExtendSeconds = 24 * 60 * 60
	}
	if j.MaxTaskEvents == 0 {
		j.MaxTaskEvents = 2000
	}
	if j.SchedulerAgingSeconds == 0 {
		j.SchedulerAgingSeconds = 300
	}
	if j.MaxQueuedTasks == 0 {
		j.MaxQueuedTasks = 4096
	}
	if j.MaxQueuedTasksPerProject == 0 {
		j.MaxQueuedTasksPerProject = 1024
	}
	if c.MCP.Path == "" {
		c.MCP.Path = "/mcp"
	}
	g := &c.ModelGateway
	if c.ConversationJobs.MaxRequestBytes == 0 {
		c.ConversationJobs.MaxRequestBytes = 1 << 20
	}
	if g.MaxRequestBytes == 0 {
		g.MaxRequestBytes = 16 << 20
	}
	if g.MaxInflight == 0 {
		g.MaxInflight = 16
	}
	if g.ConnectTimeoutSeconds == 0 {
		g.ConnectTimeoutSeconds = 30
	}
	if g.HeaderTimeoutSeconds == 0 {
		g.HeaderTimeoutSeconds = 30
	}
	if g.StreamIdleTimeoutSeconds == 0 {
		g.StreamIdleTimeoutSeconds = 300
	}
	if g.RequestTimeoutSeconds == 0 {
		g.RequestTimeoutSeconds = 1800
	}
	for k, r := range g.Routes {
		if r.MaxInflight == 0 {
			r.MaxInflight = 8
		}
		g.Routes[k] = r
	}
}
func (c Server) ValidateV02() error {
	j := c.Jobs
	profiles := map[string]ConversationProfile{}
	if j.Enabled {
		if j.MaxPartitions < 1 || j.MaxPartitions > 32 || j.MaxParallelism < 1 || j.MaxParallelism > 8 || j.MaxRequestBytes < 1 || j.MaxRequestBytes > job.MaxRequestBytes || j.MaxManifestBytes < 1 || j.MaxManifestBytes > job.MaxManifestBytes || j.MaxReduceInputBytes < 1 || j.MaxReduceInputBytes > job.MaxInputBytes || j.MaxAttemptsPerTask != 1 || j.MaxTotalRuntimeSeconds < 86400 || j.MaxTotalRuntimeSeconds > 30*24*60*60 || j.MaxDeadlineExtendSeconds < 1 || j.MaxDeadlineExtendSeconds > 24*60*60 || j.MaxDeadlineExtendSeconds > j.MaxTotalRuntimeSeconds || j.MaxTaskEvents < 100 || j.MaxTaskEvents > 100000 || j.SchedulerAgingSeconds < 1 || j.SchedulerAgingSeconds > 86400 || j.MaxQueuedTasks < 1 || j.MaxQueuedTasks > 100000 || j.MaxQueuedTasksPerProject < 1 || j.MaxQueuedTasksPerProject > j.MaxQueuedTasks {
			return fmt.Errorf("invalid job limits")
		}
		seen := map[string]bool{}
		for _, t := range j.Templates {
			key := t.Key()
			if seen[key] || !job.ValidHash(t.Digest) || !job.Ref(t.RuntimeProfile) || !job.Ref(t.PolicyRef) || !job.Ref(t.AcceptanceProfile) {
				return fmt.Errorf("invalid or duplicate job template")
			}
			seen[key] = true
		}
	}
	if c.MCP.Enabled && (!j.Enabled || c.MCP.Path != "/mcp") {
		return fmt.Errorf("MCP requires jobs and /mcp path")
	}
	if c.ModelGateway.Enabled {
		g := c.ModelGateway
		if g.PublicBaseURL != "" {
			u, e := url.Parse(g.PublicBaseURL)
			if e != nil {
				return fmt.Errorf("invalid gateway public URL")
			}
			ip := net.ParseIP(u.Hostname())
			if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != "/v1" || (u.Scheme != "https" && !(u.Scheme == "http" && c.TLS.InsecureLoopback && ip != nil && ip.IsLoopback())) {
				return fmt.Errorf("gateway public URL requires HTTPS /v1 or explicit loopback")
			}
		}
		if g.MaxRequestBytes < 1 || g.MaxRequestBytes > 16<<20 || g.MaxInflight < 1 || g.MaxInflight > 256 || g.ConnectTimeoutSeconds < 1 || g.HeaderTimeoutSeconds < 1 || g.StreamIdleTimeoutSeconds < 1 || g.RequestTimeoutSeconds < 1 || len(g.Routes) == 0 {
			return fmt.Errorf("invalid gateway limits/routes")
		}
		for _, r := range g.Routes {
			if len(r.AllowedModels) == 0 || r.MaxInflight < 1 {
				return fmt.Errorf("invalid gateway route limits")
			}
			if r.Backend == "codex_cli" {
				if r.CLI == nil || r.CLI.Executable == "" || r.CLI.Version == "" || r.BaseURL != "" || r.APIKeyFile != "" {
					return fmt.Errorf("codex_cli route requires executable/version and no upstream credentials")
				}
			} else {
				if r.Backend != "" && r.Backend != "http" || r.CLI != nil {
					return fmt.Errorf("invalid gateway backend")
				}
				u, e := url.Parse(r.BaseURL)
				if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || r.APIKeyFile == "" {
					return fmt.Errorf("invalid HTTPS gateway route")
				}
			}
		}
		for cred, route := range g.CredentialRoutes {
			if c.Credentials[cred] < 1 || g.Routes[route].BaseURL == "" || g.PublicBaseURL == "" {
				return fmt.Errorf("invalid credential gateway mapping")
			}
		}
	}
	if c.ConversationJobs.Enabled {
		cj := c.ConversationJobs
		if !j.Enabled || cj.MaxRequestBytes < 1024 || cj.MaxRequestBytes > 16<<20 {
			return fmt.Errorf("conversation jobs require enabled jobs and bounded request size")
		}
		if cj.LoopbackListen != "" {
			host, _, err := net.SplitHostPort(cj.LoopbackListen)
			ip := net.ParseIP(host)
			if err != nil || ip == nil || !ip.IsLoopback() {
				return fmt.Errorf("conversation plaintext listener must bind a literal loopback address")
			}
		}
		for _, p := range cj.Profiles {
			if !job.Ref(p.ID) || !job.Ref(p.PublicModel) || !job.Ref(p.ExecutionOwner) || !job.Ref(p.ProjectID) || profiles[p.ID].ID != "" {
				return fmt.Errorf("invalid or duplicate conversation profile")
			}
			if p.MaxInputBytes < 1 || int64(p.MaxInputBytes) > j.MaxRequestBytes || p.MaxOutputTokens < 1 || p.MaxOutputTokens > 1_000_000 || p.MaxActive < 1 || p.MaxActive > 1024 {
				return fmt.Errorf("invalid conversation profile limits")
			}
			if p.Workspace.RepositoryRef == "" || (len(p.Workspace.BaseCommit) != 40 && len(p.Workspace.BaseCommit) != 64) || func() bool { _, err := hex.DecodeString(p.Workspace.BaseCommit); return err != nil }() {
				return fmt.Errorf("conversation profile requires fixed repository and commit")
			}
			if err := p.Execution.Validate(); err != nil {
				return fmt.Errorf("invalid conversation execution: %w", err)
			}
			if p.Limits.TimeoutSeconds < 1 || p.Limits.TimeoutSeconds > 86400 || p.Limits.MaxAttemptsPerTask != 1 {
				return fmt.Errorf("invalid conversation execution limits")
			}
			profiles[p.ID] = p
		}
		if len(profiles) == 0 {
			return fmt.Errorf("conversation profiles required")
		}
		for _, p := range profiles {
			delegateOK := false
			for _, id := range c.Users {
				if id.Owner == p.ExecutionOwner && Contains(id.Projects, p.ProjectID) && Contains(id.Credentials, p.Execution.CredentialRef) && Contains(id.Scopes, "jobs:submit") && Contains(id.Scopes, "jobs:read") && Contains(id.Scopes, "jobs:cancel") {
					delegateOK = true
				}
			}
			if !delegateOK {
				return fmt.Errorf("conversation execution owner must have fixed project, credential and job permissions")
			}
			found := false
			for _, t := range j.Templates {
				if t.RuntimeProfile == p.Execution.RuntimeProfile && t.PolicyRef == p.Execution.PolicyRef && t.AcceptanceProfile == p.Execution.AcceptanceProfile && t.Digest != "" {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("conversation execution must match a trusted job template")
			}
		}
	}
	for _, id := range c.Users {
		if id.TraceOwner != "" {
			valid := false
			for _, target := range c.Users {
				if target.Owner == id.TraceOwner && Contains(target.Projects, id.ModelProject) && Contains(target.Scopes, "jobs:submit") && Contains(target.Scopes, "jobs:read") {
					valid = true
				}
			}
			if !Contains(id.Scopes, "models:invoke") || !valid {
				return fmt.Errorf("trace_owner requires an authorized job identity in model project")
			}
		}
		for _, scope := range id.Scopes {
			if !Contains([]string{"jobs:submit", "jobs:read", "jobs:cancel", "jobs:extend", "jobs:control", "jobs:retry", "models:invoke", "tasks:submit", "tasks:read", "tasks:cancel", "goals:approve", "goals:budget", "goals:constraints", "goals:propose", "conversations:submit", "conversations:read", "conversations:cancel"}, scope) {
				return fmt.Errorf("unknown identity scope")
			}
		}
		conversationScopes := Contains(id.Scopes, "conversations:submit") || Contains(id.Scopes, "conversations:read") || Contains(id.Scopes, "conversations:cancel")
		if conversationScopes {
			p, ok := profiles[id.ConversationProfile]
			if !c.ConversationJobs.Enabled || !ok || Contains(id.Scopes, "jobs:submit") || Contains(id.Scopes, "jobs:read") || Contains(id.Scopes, "jobs:cancel") {
				return fmt.Errorf("conversation identity requires a configured profile and conversation-only scopes")
			}
			if !Contains(id.Scopes, "conversations:submit") {
				return fmt.Errorf("conversation identity requires conversations:submit")
			}
			if !Contains(id.Projects, p.ProjectID) {
				return fmt.Errorf("conversation identity project must match profile")
			}
		}
		if Contains(id.Scopes, "models:invoke") && (!Contains(id.Projects, id.ModelProject) || !c.ModelGateway.Routes[id.ModelRoute].Configured()) {
			return fmt.Errorf("model identity requires authorized fixed project/route")
		}
	}
	return nil
}
func (t JobTemplate) Key() string {
	return job.TemplateKey(job.Execution{RuntimeProfile: t.RuntimeProfile, PolicyRef: t.PolicyRef, AcceptanceProfile: t.AcceptanceProfile})
}
func TemplateDigest(version string, p Policy, commands [][]string) string {
	// Normalize nested objects to sorted keys; command/argument order is preserved.
	encoded := job.JSON(struct {
		Version        string     `json:"version"`
		RuntimeVersion string     `json:"runtime_version"`
		Policy         Policy     `json:"policy"`
		Commands       [][]string `json:"commands"`
	}{job.TemplateVersion, version, p, commands})
	var canonical any
	if e := json.Unmarshal(encoded, &canonical); e != nil {
		panic(e)
	}
	return job.Hash(job.JSON(canonical))
}
func Templates(w Worker) []JobTemplate {
	var out []JobTemplate
	for profile, r := range w.Runtimes {
		for pn, p := range w.Policies {
			for vn, cmds := range w.Verifiers {
				out = append(out, JobTemplate{RuntimeProfile: profile, PolicyRef: pn, AcceptanceProfile: vn, Digest: TemplateDigest(r.Version, p, cmds)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func (r ModelRoute) Configured() bool {
	return r.BaseURL != "" || r.Backend == "codex_cli" && r.CLI != nil
}

func RouteDigest(r ModelRoute) string {
	models := append([]string(nil), r.AllowedModels...)
	sort.Strings(models)
	if r.Backend == "codex_cli" {
		return job.Hash(job.JSON(map[string]any{"version": "codex-cli-route-v1", "cli": r.CLI, "models": models}))
	}
	return job.Hash(job.JSON(map[string]any{"version": "model-route-v1", "base_url": strings.TrimRight(r.BaseURL, "/"), "models": models}))
}
