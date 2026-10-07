// Package agenthttp implements the small, self-hosted Agent Runtime HTTP
// protocol consumed by the Worker remote_api adapters. It is intended to run
// inside a dedicated OCI container with a pinned Codex/Claude installation.
package agenthttp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
)

const maxBody = 4 << 20

var runIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var imageDigestRE = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
var runProcess = process.Run
var containerCleanup = func(s *Server, id string) bool { return s.cleanupContainer(id) }

type Config struct {
	TokenFile        string
	StateDir         string
	WorkspaceRoot    string
	DockerExecutable string
	RuntimeImage     string
	CodexExecutable  string
	CodexVersion     string
	ClaudeExecutable string
	ClaudeVersion    string
}

type request struct {
	AttemptID  string   `json:"attempt_id"`
	Generation int64    `json:"generation"`
	Profile    string   `json:"profile"`
	Version    string   `json:"version"`
	Args       []string `json:"args"`
	Env        []string `json:"env"`
	CWD        string   `json:"cwd"`
	Input      string   `json:"input"`
}

type event struct {
	Sequence int64           `json:"sequence"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

type status struct {
	ID       string               `json:"id"`
	State    adapter.RuntimeState `json:"state"`
	Cleanup  adapter.CleanupState `json:"cleanup"`
	ExitCode int                  `json:"exit_code"`
	Outcome  adapter.Outcome      `json:"outcome"`
	Events   []event              `json:"events"`
	HasMore  bool                 `json:"has_more,omitempty"`
}

type run struct {
	mu          sync.Mutex
	fingerprint string
	status      status
	events      []event
	bytes       int
	cancel      context.CancelFunc
	done        chan struct{}
}

type Server struct {
	cfg  Config
	mu   sync.Mutex
	runs map[string]*run
}

func New(cfg Config) (*Server, error) {
	if cfg.TokenFile == "" || cfg.StateDir == "" || cfg.WorkspaceRoot == "" || cfg.DockerExecutable == "" || cfg.RuntimeImage == "" || cfg.CodexExecutable == "" || cfg.CodexVersion == "" || cfg.ClaudeExecutable == "" || cfg.ClaudeVersion == "" {
		return nil, errors.New("agent HTTP config requires token, state, workspace, Docker image and both pinned CLIs")
	}
	if !filepath.IsAbs(cfg.StateDir) || !filepath.IsAbs(cfg.WorkspaceRoot) || !filepath.IsAbs(cfg.DockerExecutable) || !filepath.IsAbs(cfg.CodexExecutable) || !filepath.IsAbs(cfg.ClaudeExecutable) {
		return nil, errors.New("agent HTTP paths must be absolute")
	}
	if !imageDigestRE.MatchString(cfg.RuntimeImage) {
		return nil, errors.New("agent HTTP runtime image must be pinned by digest")
	}
	tokenInfo, err := os.Stat(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	if !tokenInfo.Mode().IsRegular() || tokenInfo.Mode().Perm()&0077 != 0 {
		return nil, errors.New("agent HTTP token file must be private")
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Stat(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("agent HTTP state directory must be private")
	}
	return &Server{cfg: cfg, runs: map[string]*run{}}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, err := os.ReadFile(s.cfg.TokenFile)
	if err != nil || len(strings.TrimSpace(string(token))) == 0 {
		http.Error(w, "runtime authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	want := "Bearer " + strings.TrimSpace(string(token))
	got := r.Header.Get("Authorization")
	if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
		s.health(w, r)
		return
	}
	const prefix = "/v1/runs/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	if !runIDRE.MatchString(id) {
		http.Error(w, "invalid run ID", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.create(w, r, id)
	case http.MethodGet:
		s.inspect(w, r, id)
	case http.MethodDelete:
		s.stop(w, r, id)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func respond(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) profile(name string) (string, string, adapter.Provider, bool) {
	switch name {
	case "codex_http":
		p, _ := adapter.Lookup("codex_exec")
		return s.cfg.CodexExecutable, s.cfg.CodexVersion, p, true
	case "claude_http":
		p, _ := adapter.Lookup("claude_print")
		return s.cfg.ClaudeExecutable, s.cfg.ClaudeVersion, p, true
	default:
		return "", "", nil, false
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("profile")
	_, version, _, ok := s.profile(name)
	if !ok {
		http.Error(w, "unknown profile", http.StatusNotFound)
		return
	}
	respond(w, http.StatusOK, map[string]string{"profile": name, "version": version})
}

func (s *Server) validWorkspace(cwd string) bool {
	if !filepath.IsAbs(cwd) {
		return false
	}
	root, err := filepath.EvalSymlinks(s.cfg.WorkspaceRoot)
	if err != nil {
		return false
	}
	actual, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, actual)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) marker(id string) string { return filepath.Join(s.cfg.StateDir, id+".run") }

func (s *Server) create(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var input request
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		http.Error(w, "invalid run request", http.StatusBadRequest)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "multiple JSON values", http.StatusBadRequest)
		return
	}
	executable, version, provider, ok := s.profile(input.Profile)
	if !ok || input.Version != version || input.AttemptID != id || input.Generation < 1 || !s.validWorkspace(input.CWD) || input.Input == "" || len(input.Args) == 0 {
		http.Error(w, "run contract mismatch", http.StatusBadRequest)
		return
	}
	canonical, err := filepath.EvalSymlinks(input.CWD)
	if err != nil {
		http.Error(w, "workspace unavailable", http.StatusBadRequest)
		return
	}
	input.CWD = canonical
	if strings.ContainsAny(input.CWD, ",\n\r") {
		http.Error(w, "workspace path cannot be mounted", http.StatusBadRequest)
		return
	}
	b, _ := json.Marshal(input)
	digest := sha256.Sum256(b)
	fingerprint := hex.EncodeToString(digest[:])
	s.mu.Lock()
	if existing := s.runs[id]; existing != nil {
		s.mu.Unlock()
		if existing.fingerprint != fingerprint {
			http.Error(w, "run identity collision", http.StatusConflict)
			return
		}
		// A repeated PUT does not create another process. The Worker can inspect
		// the existing Attempt using its already-persisted reference.
		respond(w, http.StatusOK, map[string]string{"id": id})
		return
	}
	marker, err := os.OpenFile(s.marker(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		s.mu.Unlock()
		http.Error(w, "run identity already exists after service restart", http.StatusConflict)
		return
	}
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "run marker unavailable", http.StatusServiceUnavailable)
		return
	}
	_, writeErr := marker.WriteString(fingerprint + "\n")
	if writeErr == nil {
		writeErr = marker.Sync()
	}
	closeErr := marker.Close()
	if writeErr != nil || closeErr != nil {
		s.mu.Unlock()
		http.Error(w, "run marker unavailable", http.StatusServiceUnavailable)
		return
	}
	if directory, err := os.Open(s.cfg.StateDir); err == nil {
		if err := directory.Sync(); err != nil {
			_ = directory.Close()
			s.mu.Unlock()
			http.Error(w, "run marker sync failed", http.StatusServiceUnavailable)
			return
		}
		_ = directory.Close()
	} else {
		s.mu.Unlock()
		http.Error(w, "run marker sync failed", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	item := &run{fingerprint: fingerprint, status: status{ID: id, State: adapter.RuntimeStarting, Cleanup: adapter.CleanupPending}, cancel: cancel, done: make(chan struct{})}
	s.runs[id] = item
	s.mu.Unlock()
	go s.execute(ctx, item, provider, executable, input)
	respond(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) execute(ctx context.Context, item *run, provider adapter.Provider, executable string, input request) {
	defer close(item.done)
	containerName := "computecloud-attempt-" + input.AttemptID
	envFile, err := s.writeEnvFile(input)
	if err != nil {
		s.finishFailed(item)
		return
	}
	defer os.Remove(envFile)
	parser := provider.Parser(func(kind string, payload []byte) error {
		if kind == "" || !json.Valid(payload) {
			return errors.New("invalid runtime event")
		}
		item.mu.Lock()
		defer item.mu.Unlock()
		if item.bytes+len(payload) > 16<<20 {
			return errors.New("runtime event buffer exceeded")
		}
		item.events = append(item.events, event{Sequence: int64(len(item.events) + 1), Kind: kind, Payload: append([]byte(nil), payload...)})
		item.bytes += len(payload)
		return nil
	})
	lines := &adapter.Lines{Limit: maxBody, OnLine: parser.Line, OnError: item.cancel}
	dockerArgs := []string{"run", "--rm", "--pull=never", "--name", containerName, "--interactive",
		"--user", "65532:65532", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit", "256", "--network", "bridge", "--tmpfs", "/tmp:rw,nosuid,noexec,size=64m",
		"--tmpfs", "/var/lib/computecloud-agent:rw,nosuid,uid=65532,gid=65532",
		"--mount", "type=bind,source=" + input.CWD + ",target=" + input.CWD,
		"--workdir", input.CWD, "--env-file", envFile, "--entrypoint", executable, s.cfg.RuntimeImage}
	dockerArgs = append(dockerArgs, input.Args...)
	result := runProcess(ctx, s.cfg.DockerExecutable, dockerArgs, nil, s.cfg.WorkspaceRoot, strings.NewReader(input.Input), lines, io.Discard, 3*time.Second, func(int, string) error {
		item.mu.Lock()
		item.status.State = adapter.RuntimeRunning
		item.mu.Unlock()
		return nil
	})
	protocolErr := lines.Flush()
	containerClean := containerCleanup(s, input.AttemptID)
	item.mu.Lock()
	defer item.mu.Unlock()
	item.status.State = adapter.RuntimeExited
	item.status.Cleanup = adapter.CleanupUnknown
	if result.Cleanup && containerClean {
		item.status.Cleanup = adapter.CleanupConfirmed
	}
	item.status.ExitCode = result.ExitCode
	if result.Err != nil || protocolErr != nil {
		item.status.ExitCode = 1
	}
	item.status.Outcome = parser.Outcome()
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s *Server) writeEnvFile(input request) (string, error) {
	file, err := os.CreateTemp("", "computecloud-agent-env-*.env")
	if err != nil {
		return "", err
	}
	path := file.Name()
	for _, line := range input.Env {
		key, _, ok := strings.Cut(line, "=")
		if !ok || !envKeyRE.MatchString(key) || strings.ContainsAny(line, "\r\n\x00") {
			_ = file.Close()
			_ = os.Remove(path)
			return "", errors.New("invalid runtime environment")
		}
		if key == "HOME" || key == "PATH" || key == "TMPDIR" || strings.HasPrefix(key, "DOCKER_") || strings.HasPrefix(key, "COMPUTECLOUD_AGENT_") {
			continue
		}
		if _, err := file.WriteString(line + "\n"); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return "", err
		}
	}
	if _, err := file.WriteString("HOME=/var/lib/computecloud-agent\nPATH=/usr/local/bin:/usr/bin:/bin\nTMPDIR=/tmp\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (s *Server) finishFailed(item *run) {
	item.mu.Lock()
	item.status.State = adapter.RuntimeExited
	item.status.Cleanup = adapter.CleanupUnknown
	item.status.ExitCode = 1
	item.mu.Unlock()
}

func (s *Server) containerExists(id string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name := "computecloud-attempt-" + id
	output, err := exec.CommandContext(ctx, s.cfg.DockerExecutable, "container", "ls", "--all", "--format", "{{.Names}}").Output()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) cleanupContainer(id string) bool {
	exists, err := s.containerExists(id)
	if err != nil {
		return false
	}
	if exists {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err = exec.CommandContext(ctx, s.cfg.DockerExecutable, "container", "rm", "--force", "computecloud-attempt-"+id).Output()
		cancel()
		if err != nil {
			return false
		}
	}
	exists, err = s.containerExists(id)
	return err == nil && !exists
}

func (s *Server) find(id string) (*run, bool) {
	s.mu.Lock()
	item := s.runs[id]
	s.mu.Unlock()
	if item != nil {
		return item, true
	}
	_, err := os.Stat(s.marker(id))
	return nil, err == nil
}

func (s *Server) inspect(w http.ResponseWriter, r *http.Request, id string) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	item, exists := s.find(id)
	if !exists {
		http.NotFound(w, r)
		return
	}
	if item == nil {
		cleanup := adapter.CleanupUnknown
		if exists, err := s.containerExists(id); err == nil {
			if exists {
				cleanup = adapter.CleanupPending
			} else {
				cleanup = adapter.CleanupConfirmed
			}
		}
		respond(w, http.StatusOK, status{ID: id, State: adapter.RuntimeUnknown, Cleanup: cleanup})
		return
	}
	item.mu.Lock()
	current := item.status
	responseBytes := 0
	for _, event := range item.events {
		if event.Sequence <= after {
			continue
		}
		if responseBytes+len(event.Payload) > 2<<20 && len(current.Events) > 0 {
			current.HasMore = true
			break
		}
		current.Events = append(current.Events, event)
		responseBytes += len(event.Payload)
	}
	item.mu.Unlock()
	respond(w, http.StatusOK, current)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		GraceMS int64 `json:"grace_ms"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		http.Error(w, "invalid stop request", http.StatusBadRequest)
		return
	}
	item, exists := s.find(id)
	if !exists {
		http.NotFound(w, r)
		return
	}
	if item == nil {
		cleanup := adapter.CleanupUnknown
		state := adapter.RuntimeUnknown
		if containerCleanup(s, id) {
			cleanup = adapter.CleanupConfirmed
			state = adapter.RuntimeExited
		}
		respond(w, http.StatusOK, map[string]any{"state": state, "cleanup": cleanup, "term_sent": true})
		return
	}
	item.mu.Lock()
	state := item.status.State
	item.mu.Unlock()
	if state != adapter.RuntimeExited {
		item.cancel()
	}
	grace := time.Duration(body.GraceMS) * time.Millisecond
	if grace < 0 {
		grace = 0
	}
	if grace > 30*time.Second {
		grace = 30 * time.Second
	}
	select {
	case <-item.done:
	case <-time.After(grace + 5*time.Second):
		respond(w, http.StatusOK, map[string]any{"state": adapter.RuntimeUnknown, "cleanup": adapter.CleanupUnknown, "term_sent": true})
		return
	}
	item.mu.Lock()
	current := item.status
	item.mu.Unlock()
	respond(w, http.StatusOK, map[string]any{"state": current.State, "cleanup": current.Cleanup, "term_sent": state != adapter.RuntimeExited})
}

func (s *Server) ValidateVersions(ctx context.Context) error {
	for _, profile := range []string{"codex_http", "claude_http"} {
		exe, want, _, _ := s.profile(profile)
		commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		output, err := exec.CommandContext(commandCtx, s.cfg.DockerExecutable, "run", "--rm", "--network", "none", "--entrypoint", exe, s.cfg.RuntimeImage, "--version").Output()
		cancel()
		if err != nil {
			return fmt.Errorf("%s version probe failed: %w", profile, err)
		}
		found := false
		for _, field := range strings.Fields(string(output)) {
			found = found || field == want
		}
		if !found {
			return fmt.Errorf("%s version mismatch: %q", profile, strings.TrimSpace(string(output)))
		}
	}
	return nil
}
