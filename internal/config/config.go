package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	envreg "github.com/tommyxie2026-tech/computecloud/internal/environment"
	"github.com/tommyxie2026-tech/computecloud/internal/tool"
	"gopkg.in/yaml.v3"
)

type TLS struct {
	CertFile         string `yaml:"cert_file"`
	KeyFile          string `yaml:"key_file"`
	CAFile           string `yaml:"ca_file"`
	InsecureLoopback bool   `yaml:"insecure_loopback"`
}
type Identity struct {
	GoalActorID         string   `yaml:"goal_actor_id"`
	GoalActorKind       string   `yaml:"goal_actor_kind"`
	TraceOwner          string   `yaml:"trace_owner"`
	TokenFile           string   `yaml:"token_file"`
	Owner               string   `yaml:"owner"`
	Projects            []string `yaml:"projects"`
	Credentials         []string `yaml:"credentials"`
	WorkerID            string   `yaml:"worker_id"`
	Scopes              []string `yaml:"scopes"`
	ModelProject        string   `yaml:"model_project"`
	ModelRoute          string   `yaml:"model_route"`
	ConversationProfile string   `yaml:"conversation_profile"`
}
type Server struct {
	Listen           string            `yaml:"listen"`
	DataDir          string            `yaml:"data_dir"`
	TLS              TLS               `yaml:"tls"`
	Users            []Identity        `yaml:"users"`
	Workers          []Identity        `yaml:"workers"`
	Models           map[string]string `yaml:"models"`
	Credentials      map[string]int    `yaml:"credentials"`
	LeaseSeconds     int               `yaml:"lease_seconds"`
	TickMS           int               `yaml:"tick_ms"`
	MaxArtifactBytes int64             `yaml:"max_artifact_bytes"`
	MaxProjectTasks  int               `yaml:"max_project_tasks"`
	Maintenance      bool              `yaml:"maintenance"`
	HTTP             HTTP              `yaml:"http"`
	Jobs             Jobs              `yaml:"jobs"`
	MCP              MCP               `yaml:"mcp"`
	ModelGateway     ModelGateway      `yaml:"model_gateway"`
	ConversationJobs ConversationJobs  `yaml:"conversation_jobs"`
	Transport        RelayTransport    `yaml:"transport"`
}
type RelayTransport struct {
	Mode            string `yaml:"mode"`
	RelayAddress    string `yaml:"relay_address"`
	IssuerURL       string `yaml:"issuer_url"`
	TokenFile       string `yaml:"token_file"`
	CAFile          string `yaml:"ca_file"`
	ServerName      string `yaml:"server_name"`
	DirectTimeoutMS int    `yaml:"direct_timeout_ms"`
}
type Runtime struct {
	Executable    string                       `yaml:"executable"`
	Endpoint      string                       `yaml:"endpoint"`
	TokenFile     string                       `yaml:"token_file"`
	Version       string                       `yaml:"version"`
	Models        []string                     `yaml:"models"`
	Credentials   []string                     `yaml:"credentials"`
	Env           map[string]string            `yaml:"env"`
	CredentialEnv map[string]map[string]string `yaml:"credential_env"`
}
type Policy struct {
	CodexSandbox         string   `yaml:"codex_sandbox"`
	ClaudeAllowedTools   []string `yaml:"claude_allowed_tools"`
	ClaudePermissionMode string   `yaml:"claude_permission_mode"`
	GeminiApprovalMode   string   `yaml:"gemini_approval_mode" json:"GeminiApprovalMode,omitempty"`
	AllowedTools         []string `yaml:"allowed_tools" json:"AllowedTools,omitempty"`
	AllowedEnvironments  []string `yaml:"allowed_environments" json:"AllowedEnvironments,omitempty"`
}
type Worker struct {
	ID                        string                `yaml:"id"`
	Address                   string                `yaml:"server_address"`
	DataDir                   string                `yaml:"data_dir"`
	TokenFile                 string                `yaml:"token_file"`
	TLS                       TLS                   `yaml:"tls"`
	Slots                     int                   `yaml:"slots"`
	Runtimes                  map[string]Runtime    `yaml:"runtimes"`
	Repositories              map[string]string     `yaml:"repositories"`
	Policies                  map[string]Policy     `yaml:"policies"`
	Verifiers                 map[string][][]string `yaml:"verifiers"`
	StopGraceMS               int                   `yaml:"stop_grace_ms"`
	WorkspaceRetentionMS      int                   `yaml:"workspace_retention_ms"`
	WorkspaceMaxBytes         int64                 `yaml:"workspace_max_bytes"`
	WorkspaceWarmSlots        int                   `yaml:"workspace_warm_slots"`
	WorkspaceCacheMaxBytes    int64                 `yaml:"workspace_cache_max_bytes"`
	WorkspaceCacheRetentionMS int64                 `yaml:"workspace_cache_retention_ms"`
	Transport                 RelayTransport        `yaml:"transport"`
}
type Client struct {
	Address   string `yaml:"address"`
	TokenFile string `yaml:"token_file"`
	TLS       TLS    `yaml:"tls"`
	HTTPURL   string `yaml:"http_url"`
}
type Config struct {
	Server Server `yaml:"server"`
	Worker Worker `yaml:"worker"`
	Client Client `yaml:"client"`
}

func Load(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	base, e := filepath.Abs(filepath.Dir(path))
	if e != nil {
		return c, e
	}
	abs := func(p string) string {
		if p != "" && !filepath.IsAbs(p) {
			return filepath.Join(base, p)
		}
		return p
	}
	tlsPaths := func(t *TLS) { t.CertFile = abs(t.CertFile); t.KeyFile = abs(t.KeyFile); t.CAFile = abs(t.CAFile) }
	c.Server.DataDir = abs(c.Server.DataDir)
	c.Worker.DataDir = abs(c.Worker.DataDir)
	c.Worker.TokenFile = abs(c.Worker.TokenFile)
	c.Client.TokenFile = abs(c.Client.TokenFile)
	c.Server.Transport.TokenFile = abs(c.Server.Transport.TokenFile)
	c.Server.Transport.CAFile = abs(c.Server.Transport.CAFile)
	c.Worker.Transport.TokenFile = abs(c.Worker.Transport.TokenFile)
	c.Worker.Transport.CAFile = abs(c.Worker.Transport.CAFile)
	tlsPaths(&c.Server.TLS)
	tlsPaths(&c.Worker.TLS)
	tlsPaths(&c.Client.TLS)
	for i := range c.Server.Users {
		c.Server.Users[i].TokenFile = abs(c.Server.Users[i].TokenFile)
	}
	for i := range c.Server.Workers {
		c.Server.Workers[i].TokenFile = abs(c.Server.Workers[i].TokenFile)
	}
	for key, route := range c.Server.ModelGateway.Routes {
		route.APIKeyFile = abs(route.APIKeyFile)
		if route.CLI != nil && strings.ContainsRune(route.CLI.Executable, '/') {
			route.CLI.Executable = abs(route.CLI.Executable)
		}
		c.Server.ModelGateway.Routes[key] = route
	}
	for k, v := range c.Worker.Repositories {
		c.Worker.Repositories[k] = abs(v)
	}
	for k, v := range c.Worker.Runtimes {
		if strings.ContainsRune(v.Executable, '/') {
			v.Executable = abs(v.Executable)
		}
		v.TokenFile = abs(v.TokenFile)
		for ref, env := range v.CredentialEnv {
			for key, file := range env {
				env[key] = abs(file)
			}
			v.CredentialEnv[ref] = env
		}
		c.Worker.Runtimes[k] = v
	}
	if c.Server.Listen == "" {
		c.Server.Listen = "127.0.0.1:7443"
	}
	if c.Server.LeaseSeconds == 0 {
		c.Server.LeaseSeconds = 60
	}
	if c.Server.TickMS == 0 {
		c.Server.TickMS = 500
	}
	if c.Server.MaxArtifactBytes == 0 {
		c.Server.MaxArtifactBytes = 32 << 20
	}
	if c.Server.MaxProjectTasks == 0 {
		c.Server.MaxProjectTasks = 8
	}
	if c.Worker.Slots == 0 {
		c.Worker.Slots = 2
	}
	if c.Worker.StopGraceMS == 0 {
		c.Worker.StopGraceMS = 3000
	}
	if c.Worker.WorkspaceRetentionMS == 0 {
		c.Worker.WorkspaceRetentionMS = 24 * 60 * 60 * 1000
	}
	c.Server.DefaultV02()
	return c, nil
}
func Token(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 24 {
		return "", errors.New("token must contain at least 24 characters")
	}
	return s, nil
}
func Contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func (c Server) Validate() error {
	if err := c.Transport.Validate(true, c.TLS); err != nil {
		return err
	}
	if c.DataDir == "" || c.LeaseSeconds < 3 || c.TickMS < 10 || c.MaxArtifactBytes < 1 || c.MaxProjectTasks < 1 {
		return errors.New("invalid server limits/data_dir")
	}
	for k, v := range c.Credentials {
		if k == "" || v < 1 {
			return fmt.Errorf("invalid credential limit %q", k)
		}
	}
	return c.ValidateV02()
}
func (c Worker) Validate() error {
	if err := c.Transport.Validate(false, c.TLS); err != nil {
		return err
	}
	if c.ID == "" || c.DataDir == "" || c.Address == "" || c.Slots < 1 || len(c.Runtimes) == 0 {
		return errors.New("worker id/data_dir/address/slots/runtimes required")
	}
	if c.WorkspaceRetentionMS < 0 || c.WorkspaceMaxBytes < 0 {
		return errors.New("worker workspace retention/quota must be non-negative")
	}
	if c.WorkspaceWarmSlots < 0 || c.WorkspaceWarmSlots > 2 || c.WorkspaceCacheMaxBytes < 0 || c.WorkspaceCacheRetentionMS < 0 || c.WorkspaceCacheRetentionMS > 365*24*60*60*1000 {
		return errors.New("invalid workspace cache policy")
	}
	for ref, path := range c.Repositories {
		if ref == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("invalid repository %q", ref)
		}
	}
	for ref, policy := range c.Policies {
		if policy.GeminiApprovalMode != "" && policy.GeminiApprovalMode != "plan" && policy.GeminiApprovalMode != "auto_edit" {
			return fmt.Errorf("invalid gemini approval mode in policy %q", ref)
		}
		seen := map[string]bool{}
		for _, name := range policy.AllowedTools {
			if !tool.ValidName(name) || seen[name] {
				return fmt.Errorf("invalid or duplicate allowed tool %q in policy %q", name, ref)
			}
			seen[name] = true
		}
		seenEnvironment := map[string]bool{}
		for _, name := range policy.AllowedEnvironments {
			if !envreg.ValidName(name) || seenEnvironment[name] {
				return fmt.Errorf("invalid or duplicate allowed environment %q in policy %q", name, ref)
			}
			seenEnvironment[name] = true
		}
	}
	return nil
}
func (t RelayTransport) Enabled() bool { return t.Mode == "direct_then_relay" }
func (t RelayTransport) DirectTimeout() int {
	if t.DirectTimeoutMS == 0 {
		return 1000
	}
	return t.DirectTimeoutMS
}
func (t RelayTransport) Validate(server bool, innerTLS TLS) error {
	if t.Mode == "" || t.Mode == "direct" {
		if t.RelayAddress != "" || t.IssuerURL != "" || t.TokenFile != "" || t.CAFile != "" || t.ServerName != "" || t.DirectTimeoutMS != 0 {
			return errors.New("Relay fields require explicit direct_then_relay mode")
		}
		return nil
	}
	if t.Mode != "direct_then_relay" || innerTLS.InsecureLoopback || !filepath.IsAbs(t.TokenFile) || !filepath.IsAbs(t.CAFile) || t.DirectTimeout() < 1 || t.DirectTimeout() > 10000 {
		return errors.New("invalid Relay mode, inner TLS or credential paths")
	}
	host, port, err := net.SplitHostPort(t.RelayAddress)
	if err != nil || host == "" || port == "" {
		return errors.New("Relay address must be host:port")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return errors.New("Relay address must use a valid TCP port")
	}
	u, err := url.Parse(t.IssuerURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Hostname() != host || u.Port() == "" {
		return errors.New("Relay issuer URL must be an HTTPS origin")
	}
	if _, err := net.LookupPort("tcp", u.Port()); err != nil {
		return errors.New("Relay issuer URL must use a valid TCP port")
	}
	if server && t.DirectTimeoutMS != 0 {
		return errors.New("Relay Server must not set direct timeout")
	}
	return nil
}
func JSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
