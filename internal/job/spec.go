// Package job defines the transport-independent, explicitly partitioned Job contract.
package job

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	jobv1 "github.com/tommyxie2026-tech/computecloud/api/job/v1"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
)

const MaxRequestBytes = 256 << 10
const MaxManifestBytes = 32 << 10
const MaxInputBytes = 128 << 20
const TemplateVersion = "computecloud-job-io-v1"

type Input struct {
	Text string `json:"text"`
}
type Workspace struct {
	RepositoryRef string `json:"repository_ref"`
	BaseCommit    string `json:"base_commit"`
}
type Execution struct {
	Engine            string `json:"engine"`
	RuntimeProfile    string `json:"runtime_profile"`
	Model             string `json:"model"`
	CredentialRef     string `json:"credential_ref"`
	PolicyRef         string `json:"policy_ref"`
	AcceptanceProfile string `json:"acceptance_profile"`
	ReplaySafe        bool   `json:"replay_safe,omitempty"`
}
type Partition struct {
	Key        string    `json:"key"`
	ScopePaths []string  `json:"scope_paths"`
	Input      Input     `json:"input"`
	Execution  Execution `json:"execution"`
}
type Map struct {
	Parallelism int         `json:"parallelism"`
	Partitions  []Partition `json:"partitions"`
}
type Reduce struct {
	Strategy  string    `json:"strategy"`
	Input     Input     `json:"input"`
	Execution Execution `json:"execution"`
}
type Limits struct {
	TimeoutSeconds     int64 `json:"timeout_seconds"`
	MaxAttemptsPerTask int   `json:"max_attempts_per_task"`
}
type Spec struct {
	SchemaVersion string     `json:"schema_version"`
	ProjectID     string     `json:"project_id"`
	Mode          string     `json:"mode"`
	Workspace     Workspace  `json:"workspace"`
	Input         Input      `json:"input"`
	Execution     *Execution `json:"execution,omitempty"`
	Map           *Map       `json:"map,omitempty"`
	Reduce        *Reduce    `json:"reduce,omitempty"`
	Limits        Limits     `json:"limits"`
}
type Frozen struct {
	Spec         Spec              `json:"spec"`
	Digests      map[string]string `json:"template_digests"`
	Routes       map[string]string `json:"gateway_routes,omitempty"`
	RouteDigests map[string]string `json:"gateway_route_digests,omitempty"`
}
type Manifest struct {
	Version    string         `json:"version"`
	BaseCommit string         `json:"base_commit"`
	Items      []ManifestItem `json:"items"`
}
type ManifestItem struct {
	PartitionKey   string   `json:"partition_key"`
	TaskID         string   `json:"task_id"`
	AttemptID      string   `json:"attempt_id"`
	Generation     int64    `json:"generation"`
	TemplateDigest string   `json:"template_digest"`
	ScopePaths     []string `json:"scope_paths"`
	ArtifactID     string   `json:"artifact_id"`
	SHA256         string   `json:"sha256"`
	Size           int64    `json:"size"`
}

var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var commitRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func JSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func TemplateKey(e Execution) string {
	return Hash(JSON([]string{e.RuntimeProfile, e.PolicyRef, e.AcceptanceProfile}))
}
func ValidHash(s string) bool { return hashRE.MatchString(s) }
func ValidKey(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func ValidPath(p string) bool {
	if len(p) == 0 || len(p) > 1024 || p == "." || strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." || strings.EqualFold(s, ".git") {
			return false
		}
	}
	return true
}
func InScope(p string, scopes []string) bool {
	for _, s := range scopes {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}
func Ref(s string) bool {
	return len(s) > 0 && len(s) <= 128 && !strings.ContainsAny(s, "\x00\r\n") && !strings.HasPrefix(s, "-")
}
func (e Execution) Validate() error {
	if (e.Engine != "codex" || e.RuntimeProfile != "codex_exec") && (e.Engine != "claude" || e.RuntimeProfile != "claude_print") {
		return fmt.Errorf("unsupported engine/runtime")
	}
	for _, v := range []string{e.Model, e.CredentialRef, e.PolicyRef, e.AcceptanceProfile} {
		if !Ref(v) {
			return fmt.Errorf("explicit model, credential, policy and acceptance references required")
		}
	}
	return nil
}
func (s *Spec) Validate(maxParts, maxParallel int) error {
	if s.SchemaVersion != "v0.2" || !Ref(s.ProjectID) || !Ref(s.Workspace.RepositoryRef) || !commitRE.MatchString(s.Workspace.BaseCommit) {
		return fmt.Errorf("version, project and fixed repository commit required")
	}
	if s.Limits.TimeoutSeconds < 1 || s.Limits.TimeoutSeconds > 86400 || s.Limits.MaxAttemptsPerTask < 1 || s.Limits.MaxAttemptsPerTask > 3 {
		return fmt.Errorf("invalid deadline or max_attempts_per_task")
	}
	validInput := func(i Input) bool { return len(i.Text) > 0 && len(i.Text) <= 64<<10 }
	if !validInput(s.Input) {
		return fmt.Errorf("input must contain 1..65536 UTF-8 bytes")
	}
	if s.Mode == "single" {
		if s.Execution == nil || s.Map != nil || s.Reduce != nil {
			return fmt.Errorf("single requires execution only")
		}
		if e := s.Execution.Validate(); e != nil {
			return e
		}
		if s.Limits.MaxAttemptsPerTask > 1 && !s.Execution.ReplaySafe {
			return fmt.Errorf("automatic retry requires replay_safe execution")
		}
		return nil
	}
	if s.Mode != "map_reduce" || s.Execution != nil || s.Map == nil || s.Reduce == nil {
		return fmt.Errorf("map_reduce requires map and reduce")
	}
	if s.Map.Parallelism < 1 || s.Map.Parallelism > maxParallel || len(s.Map.Partitions) < 1 || len(s.Map.Partitions) > maxParts {
		return fmt.Errorf("partition or parallelism limit")
	}
	if (s.Reduce.Strategy != "report_merge_v1" && s.Reduce.Strategy != "patch_merge_v1") || !validInput(s.Reduce.Input) {
		return fmt.Errorf("invalid reduce strategy/input")
	}
	if e := s.Reduce.Execution.Validate(); e != nil {
		return e
	}
	seen := map[string]bool{}
	var writeScopes []string
	for i := range s.Map.Partitions {
		p := &s.Map.Partitions[i]
		if !keyRE.MatchString(p.Key) || seen[p.Key] || !validInput(p.Input) {
			return fmt.Errorf("invalid or duplicate partition")
		}
		seen[p.Key] = true
		if e := p.Execution.Validate(); e != nil {
			return e
		}
		if len(p.ScopePaths) < 1 || len(p.ScopePaths) > 64 {
			return fmt.Errorf("scope paths required")
		}
		sort.Strings(p.ScopePaths)
		for n, v := range p.ScopePaths {
			if !ValidPath(v) || (n > 0 && v == p.ScopePaths[n-1]) {
				return fmt.Errorf("invalid or duplicate scope path")
			}
			if s.Reduce.Strategy == "patch_merge_v1" {
				for _, prev := range writeScopes {
					if InScope(v, []string{prev}) || InScope(prev, []string{v}) {
						return fmt.Errorf("overlapping patch scopes")
					}
				}
			}
		}
		if s.Reduce.Strategy == "patch_merge_v1" {
			writeScopes = append(writeScopes, p.ScopePaths...)
		}
	}
	sort.Slice(s.Map.Partitions, func(i, j int) bool { return s.Map.Partitions[i].Key < s.Map.Partitions[j].Key })
	if s.Limits.MaxAttemptsPerTask > 1 {
		for _, ex := range s.Executions() {
			if !ex.ReplaySafe {
				return fmt.Errorf("automatic retry requires every execution to be replay_safe")
			}
		}
	}
	return nil
}
func Decode(b []byte, maxParts, maxParallel int) (Spec, error) {
	var s Spec
	if len(b) > MaxRequestBytes {
		return s, fmt.Errorf("request too large")
	}
	if e := jsonutil.Decode(b, &s); e != nil {
		return s, e
	}
	var instance any
	if e := json.Unmarshal(b, &instance); e != nil {
		return s, e
	}
	if e := requestSchema().Validate(instance); e != nil {
		return s, fmt.Errorf("JOB_SCHEMA_INVALID")
	}
	if e := s.Validate(maxParts, maxParallel); e != nil {
		return s, e
	}
	return s, nil
}

var requestSchema = sync.OnceValue(func() *jsonschema.Resolved {
	var s jsonschema.Schema
	if e := json.Unmarshal(jobv1.Schema, &s); e != nil {
		panic(e)
	}
	r, e := s.Resolve(nil)
	if e != nil {
		panic(e)
	}
	return r
})

func (s Spec) ExecutionFor(stage, key string) (Execution, bool) {
	if stage == "single" && s.Execution != nil {
		return *s.Execution, true
	}
	if stage == "reduce" && s.Reduce != nil {
		return s.Reduce.Execution, true
	}
	if stage == "map" && s.Map != nil {
		for _, p := range s.Map.Partitions {
			if p.Key == key {
				return p.Execution, true
			}
		}
	}
	return Execution{}, false
}

func (s Spec) Executions() []Execution {
	if s.Execution != nil {
		return []Execution{*s.Execution}
	}
	var out []Execution
	for _, p := range s.Map.Partitions {
		out = append(out, p.Execution)
	}
	return append(out, s.Reduce.Execution)
}
func (e Execution) Task(s Spec, key, text string) *pb.TaskSpec {
	return &pb.TaskSpec{ProjectId: s.ProjectID, IdempotencyKey: key, Engine: e.Engine, RuntimeProfile: e.RuntimeProfile, Model: e.Model, CredentialRef: e.CredentialRef, Workspace: &pb.Workspace{RepositoryRef: s.Workspace.RepositoryRef, BaseCommit: s.Workspace.BaseCommit, IsolationProfile: "trusted-worktree-process"}, Input: &pb.Input{Text: text}, PolicyRef: e.PolicyRef, AcceptanceProfile: e.AcceptanceProfile, TimeoutSeconds: s.Limits.TimeoutSeconds, RequiredCapabilities: []string{"event_stream", "cancel", "job_io_v1"}}
}
