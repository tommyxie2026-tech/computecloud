package config

import (
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
	Enabled                bool          `yaml:"enabled"`
	MaxPartitions          int           `yaml:"max_partitions"`
	MaxParallelism         int           `yaml:"max_parallelism"`
	RecommendedParallelism int           `yaml:"recommended_parallelism"`
	MaxRequestBytes        int64         `yaml:"max_request_bytes"`
	MaxManifestBytes       int64         `yaml:"max_manifest_bytes"`
	MaxReduceInputBytes    int64         `yaml:"max_reduce_input_bytes"`
	MaxAttemptsPerTask       int           `yaml:"max_attempts_per_task"`
	MaxTotalRuntimeSeconds   int64         `yaml:"max_total_runtime_seconds"`
	MaxDeadlineExtendSeconds int64         `yaml:"max_deadline_extend_seconds"`
	MaxTaskEvents            int           `yaml:"max_task_events"`
	Templates                []JobTemplate `yaml:"templates"`
}
type ModelRoute struct {
	BaseURL       string   `yaml:"base_url"`
	APIKeyFile    string   `yaml:"api_key_file"`
	AllowedModels []string `yaml:"allowed_models"`
	MaxInflight   int      `yaml:"max_inflight"`
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
	if c.MCP.Path == "" {
		c.MCP.Path = "/mcp"
	}
	g := &c.ModelGateway
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
	if j.Enabled {
		if j.MaxPartitions < 1 || j.MaxPartitions > 32 || j.MaxParallelism < 1 || j.MaxParallelism > 8 || j.MaxRequestBytes < 1 || j.MaxRequestBytes > job.MaxRequestBytes || j.MaxManifestBytes < 1 || j.MaxManifestBytes > job.MaxManifestBytes || j.MaxReduceInputBytes < 1 || j.MaxReduceInputBytes > job.MaxInputBytes || j.MaxAttemptsPerTask != 1 || j.MaxTotalRuntimeSeconds < 86400 || j.MaxTotalRuntimeSeconds > 30*24*60*60 || j.MaxDeadlineExtendSeconds < 1 || j.MaxDeadlineExtendSeconds > 24*60*60 || j.MaxDeadlineExtendSeconds > j.MaxTotalRuntimeSeconds || j.MaxTaskEvents < 100 || j.MaxTaskEvents > 100000 {
			return fmt.Errorf("invalid job limits")
		}
		seen := map[string]bool{}
		for _, t := range j.Templates {
			key := t.Key()
			if seen[key] || !job.ValidHash(t.Digest) || !job.Ref(t.PolicyRef) || !job.Ref(t.AcceptanceProfile) || (t.RuntimeProfile != "codex_exec" && t.RuntimeProfile != "claude_print") {
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
			u, e := url.Parse(r.BaseURL)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || r.APIKeyFile == "" || len(r.AllowedModels) == 0 || r.MaxInflight < 1 {
				return fmt.Errorf("invalid HTTPS gateway route")
			}
		}
		for cred, route := range g.CredentialRoutes {
			if c.Credentials[cred] < 1 || g.Routes[route].BaseURL == "" || g.PublicBaseURL == "" {
				return fmt.Errorf("invalid credential gateway mapping")
			}
		}
	}
	for _, id := range c.Users {
		for _, scope := range id.Scopes {
			if !Contains([]string{"jobs:submit", "jobs:read", "jobs:cancel", "jobs:extend", "models:invoke", "tasks:submit", "tasks:read", "tasks:cancel"}, scope) {
				return fmt.Errorf("unknown identity scope")
			}
		}
		if Contains(id.Scopes, "models:invoke") && (!Contains(id.Projects, id.ModelProject) || c.ModelGateway.Routes[id.ModelRoute].BaseURL == "") {
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

func RouteDigest(r ModelRoute) string {
	models := append([]string(nil), r.AllowedModels...)
	sort.Strings(models)
	return job.Hash(job.JSON(map[string]any{"version": "model-route-v1", "base_url": strings.TrimRight(r.BaseURL, "/"), "models": models}))
}
