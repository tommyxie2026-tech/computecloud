package agenthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
)

func testServer(t *testing.T) (*Server, Config) {
	t.Helper()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{TokenFile: token, StateDir: filepath.Join(root, "state"), WorkspaceRoot: root,
		DockerExecutable: "/usr/bin/docker", RuntimeImage: "example/agent@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CodexExecutable: "/bin/sh", CodexVersion: "0.160.1", ClaudeExecutable: "/bin/sh", ClaudeVersion: "2.1.292"}
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return server, cfg
}

func call(t *testing.T, server *Server, method, path string, payload any, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	if auth {
		request.Header.Set("Authorization", "Bearer secret")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func TestRunLifecycleIdempotencyAndRestartFence(t *testing.T) {
	server, cfg := testServer(t)
	original := runProcess
	cleanupOriginal := containerCleanup
	dockerReadyOriginal := dockerReady
	containerCleanup = func(*Server, string) bool { return true }
	dockerReady = func(*Server) bool { return true }
	canonicalWorkspace, err := filepath.EvalSymlinks(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	runProcess = func(_ context.Context, executable string, args, _ []string, _ string, _ io.Reader, stdout, _ io.Writer, _ time.Duration, onStart func(int, string) error) process.Result {
		if executable != "/usr/bin/docker" || len(args) < 5 || args[0] != "run" || args[4] != "computecloud-attempt-attempt-1" {
			t.Errorf("unexpected Docker command: %s %v", executable, args)
		}
		joined := strings.Join(args, " ")
		for _, required := range []string{"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit 256", "--mount type=bind,source=" + canonicalWorkspace + ",target=" + canonicalWorkspace} {
			if !strings.Contains(joined, required) {
				t.Errorf("missing isolation flag %q in %v", required, args)
			}
		}
		for _, arg := range args {
			if arg == "--privileged" || strings.Contains(arg, cfg.TokenFile) || strings.Contains(arg, "/var/run/docker.sock") {
				t.Errorf("unsafe Docker argument: %s", arg)
			}
		}
		if err := onStart(42, "test"); err != nil {
			return process.Result{Err: err}
		}
		_, _ = stdout.Write([]byte("{\"type\":\"turn.completed\",\"usage\":{}}\n"))
		return process.Result{Cleanup: true, ExitCode: 0}
	}
	t.Cleanup(func() { runProcess = original; containerCleanup = cleanupOriginal; dockerReady = dockerReadyOriginal })
	if got := call(t, server, http.MethodGet, "/v1/health?profile=codex_http", nil, false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated health status=%d", got)
	}
	if got := call(t, server, http.MethodGet, "/v1/health?profile=codex_http", nil, true).Code; got != http.StatusOK {
		t.Fatalf("health status=%d", got)
	}
	runRequest := request{AttemptID: "attempt-1", Generation: 1, Profile: "codex_http", Version: "0.160.1",
		Args: []string{"-c", "printf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'"}, CWD: cfg.WorkspaceRoot, Input: "prompt"}
	if got := call(t, server, http.MethodPut, "/v1/runs/attempt-1", runRequest, true).Code; got != http.StatusCreated {
		t.Fatalf("create status=%d", got)
	}
	if got := call(t, server, http.MethodPut, "/v1/runs/attempt-1", runRequest, true).Code; got != http.StatusOK {
		t.Fatalf("repeat status=%d", got)
	}
	collision := runRequest
	collision.Input = "different"
	if got := call(t, server, http.MethodPut, "/v1/runs/attempt-1", collision, true).Code; got != http.StatusConflict {
		t.Fatalf("collision status=%d", got)
	}
	var got status
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := call(t, server, http.MethodGet, "/v1/runs/attempt-1?after=0", nil, true)
		if response.Code != http.StatusOK {
			t.Fatalf("inspect status=%d", response.Code)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.State == adapter.RuntimeExited {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.State != adapter.RuntimeExited || got.Cleanup != adapter.CleanupConfirmed || got.ExitCode != 0 || !got.Outcome.Final || !got.Outcome.Success || len(got.Events) == 0 {
		t.Fatalf("terminal status=%+v", got)
	}
	if got := call(t, server, http.MethodDelete, "/v1/runs/attempt-1", map[string]int64{"grace_ms": 10}, true).Code; got != http.StatusOK {
		t.Fatalf("stop status=%d", got)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, restarted, http.MethodGet, "/v1/runs/attempt-1?after=0", nil, true)
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != adapter.RuntimeUnknown || (got.Cleanup != adapter.CleanupUnknown && got.Cleanup != adapter.CleanupConfirmed) {
		t.Fatalf("restart invented runtime outcome or pending cleanup: %+v", got)
	}
	if got := call(t, restarted, http.MethodPut, "/v1/runs/attempt-1", runRequest, true).Code; got != http.StatusConflict {
		t.Fatalf("restart replay status=%d", got)
	}
}

func TestEnvironmentFileDropsControllerVariables(t *testing.T) {
	server, _ := testServer(t)
	path, err := server.writeEnvFile(request{AttemptID: "a", Env: []string{"HOME=/host", "PATH=/host/bin", "DOCKER_HOST=tcp://daemon", "OPENAI_API_KEY=secret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, "/host") || strings.Contains(text, "DOCKER_HOST") || !strings.Contains(text, "OPENAI_API_KEY=secret") || !strings.Contains(text, "HOME=/var/lib/computecloud-agent") {
		t.Fatalf("unsafe environment file: %q", text)
	}
	if _, err := server.writeEnvFile(request{AttemptID: "a", Env: []string{"TOKEN=secret\nOTHER=value"}}); err == nil {
		t.Fatal("accepted newline in secret")
	}
}

func TestRunRejectsPathAndVersionEscapes(t *testing.T) {
	server, cfg := testServer(t)
	input := request{AttemptID: "a", Generation: 1, Profile: "claude_http", Version: "wrong", Args: []string{"-c", "true"}, CWD: cfg.WorkspaceRoot, Input: "prompt"}
	if got := call(t, server, http.MethodPut, "/v1/runs/a", input, true).Code; got != http.StatusBadRequest {
		t.Fatalf("accepted version mismatch: %d", got)
	}
	input.Version = cfg.ClaudeVersion
	input.CWD = "/"
	if got := call(t, server, http.MethodPut, "/v1/runs/a", input, true).Code; got != http.StatusBadRequest {
		t.Fatalf("accepted workspace escape: %d", got)
	}
	input.CWD = cfg.WorkspaceRoot
	if got := call(t, server, http.MethodPut, "/v1/runs/other", input, true).Code; got != http.StatusBadRequest {
		t.Fatalf("accepted identity mismatch: %d", got)
	}
}

func TestEnvironmentReleaseFencesLateRun(t *testing.T) {
	server, cfg := testServer(t)
	previous := containerCleanup
	containerCleanup = func(*Server, string) bool { return true }
	t.Cleanup(func() { containerCleanup = previous })
	response := call(t, server, http.MethodDelete, "/v1/environments/attempt-late", nil, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"cleanup":"CONFIRMED"`) {
		t.Fatalf("environment release=%d %s", response.Code, response.Body.String())
	}
	input := request{AttemptID: "attempt-late", Generation: 1, Profile: "codex_http", Version: cfg.CodexVersion,
		Args: []string{"exec", "--json"}, CWD: cfg.WorkspaceRoot, Input: "prompt"}
	if got := call(t, server, http.MethodPut, "/v1/runs/attempt-late", input, true).Code; got != http.StatusConflict {
		t.Fatalf("late run replay status=%d", got)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(t, restarted, http.MethodPut, "/v1/runs/attempt-late", input, true).Code; got != http.StatusConflict {
		t.Fatalf("late run after restart status=%d", got)
	}
}

func TestEnvironmentDockerUnavailableKeepsUnknownAndFencesReplay(t *testing.T) {
	server, cfg := testServer(t)
	server.cfg.DockerExecutable = filepath.Join(cfg.StateDir, "missing-docker")
	if got := call(t, server, http.MethodGet, "/v1/health?profile=codex_http", nil, true).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("health without Docker status=%d", got)
	}
	if got := call(t, server, http.MethodGet, "/v1/environments/no-docker", nil, true).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("inspect without Docker status=%d", got)
	}
	if got := call(t, server, http.MethodDelete, "/v1/environments/no-docker", nil, true).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("cleanup without Docker status=%d", got)
	}
	input := request{AttemptID: "no-docker", Generation: 1, Profile: "codex_http", Version: cfg.CodexVersion,
		Args: []string{"exec", "--json"}, CWD: cfg.WorkspaceRoot, Input: "prompt"}
	if got := call(t, server, http.MethodPut, "/v1/runs/no-docker", input, true).Code; got != http.StatusConflict {
		t.Fatalf("replay after unknown cleanup status=%d", got)
	}
}
