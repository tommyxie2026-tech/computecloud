package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

func testHTTPRuntime(t *testing.T, handler http.Handler) config.Runtime {
	t.Helper()
	dir := t.TempDir()
	original := newHTTPClient
	newHTTPClient = func(r config.Runtime) (*http.Client, string, error) {
		_, socket, err := httpClient(r)
		if err != nil {
			return nil, "", err
		}
		return &http.Client{Transport: handlerTransport{handler}}, socket, nil
	}
	t.Cleanup(func() { newHTTPClient = original })
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return config.Runtime{Endpoint: "unix:///tmp/runtime.sock", TokenFile: token, Version: "0.160.1"}
}

func TestHTTPRuntimeLifecycleAndEventCursor(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"profile":"codex_http","version":"0.160.1","isolation":"container"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/runs/run-1":
			var request httpRunRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.AttemptID != "run-1" || request.Generation != 1 || request.Profile != "codex_http" || request.Version != "0.160.1" || request.CWD != "/workspace" || request.Input != "hello" || !reflect.DeepEqual(request.Args, []string{"exec", "--json", "--sandbox", "read-only", "--model", "test-model", "-"}) {
				t.Errorf("unexpected run request: %+v", request)
			}
			_, _ = w.Write([]byte(`{"id":"run-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-1":
			if r.URL.Query().Get("after") != "0" {
				t.Errorf("unexpected cursor: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"id":"run-1","state":"EXITED","cleanup":"CONFIRMED","exit_code":0,"outcome":{"final":true,"success":true},"events":[{"sequence":1,"kind":"message.delta","payload":{"text":"done"}}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/runs/run-1":
			_, _ = w.Write([]byte(`{"state":"EXITED","cleanup":"CONFIRMED","term_sent":false,"kill_sent":false}`))
		default:
			http.NotFound(w, r)
		}
	})
	runtime := testHTTPRuntime(t, handler)
	p, ok := Lookup("codex_http")
	if !ok {
		t.Fatal("HTTP provider not registered")
	}
	if err := p.Probe(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	var events []string
	prepared, err := p.Prepare(PrepareRequest{
		AttemptID: "run-1", Generation: 1,
		Runtime: runtime, Spec: &pb.TaskSpec{Model: "test-model"}, Policy: config.Policy{CodexSandbox: "read-only"},
		CWD: "/workspace", Input: "hello", Emit: func(kind string, payload []byte) error { events = append(events, kind+":"+string(payload)); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var persisted ExecutionRef
	result := p.Start(context.Background(), prepared, func(ref ExecutionRef) error { persisted = ref; return nil })
	if result.Err != nil || result.ProtocolErr != nil || result.State != RuntimeExited || result.Cleanup != CleanupConfirmed || !result.Outcome.Final || !result.Outcome.Success {
		t.Fatalf("result=%+v", result)
	}
	if persisted.ID != "run-1" || persisted.PID != 0 || persisted.Transport != "remote_api" {
		t.Fatalf("persisted=%+v", persisted)
	}
	if !reflect.DeepEqual(events, []string{`message.delta:{"text":"done"}`}) {
		t.Fatalf("events=%v", events)
	}
	inspected, err := p.Inspect(context.Background(), runtime, persisted)
	if err != nil || inspected.State != RuntimeExited || inspected.Cleanup != CleanupConfirmed {
		t.Fatalf("inspect=%+v err=%v", inspected, err)
	}
	stopped, err := p.Stop(context.Background(), runtime, persisted, time.Second)
	if err != nil || stopped.State != RuntimeExited || stopped.Cleanup != CleanupConfirmed {
		t.Fatalf("stop=%+v err=%v", stopped, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 5 {
		t.Fatalf("requests=%v", requests)
	}
}

func TestHTTPRuntimeRejectsIdentityAndForeignReference(t *testing.T) {
	runtime := testHTTPRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profile":"claude_http","version":"2.1.292"}`))
	}))
	p, _ := Lookup("codex_http")
	if err := p.Probe(context.Background(), runtime); err == nil {
		t.Fatal("accepted different runtime identity")
	}
	if _, err := p.Inspect(context.Background(), runtime, ExecutionRef{Provider: "claude_http", Transport: "remote_api", ID: "other"}); err == nil {
		t.Fatal("accepted foreign reference")
	}
	if _, err := p.Stop(context.Background(), runtime, ExecutionRef{Provider: "codex_http", Transport: "local_cli", ID: "other"}, 0); err == nil {
		t.Fatal("accepted local reference")
	}
	bad := runtime
	bad.Endpoint = "http://127.0.0.1:1234"
	if _, err := p.Prepare(PrepareRequest{Runtime: bad}); err == nil {
		t.Fatal("accepted non-unix endpoint")
	}
}

func TestHTTPRuntimeCreateFailureKeepsPersistedAttemptReference(t *testing.T) {
	runtime := testHTTPRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	}))
	p, _ := Lookup("claude_http")
	prepared, err := p.Prepare(PrepareRequest{AttemptID: "attempt-2", Generation: 3,
		Runtime: runtime, Spec: &pb.TaskSpec{Model: "test-model"}, Policy: config.Policy{ClaudePermissionMode: "dontAsk"},
		CWD: "/workspace", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var persisted ExecutionRef
	result := p.Start(context.Background(), prepared, func(ref ExecutionRef) error { persisted = ref; return nil })
	if result.Err == nil || result.Ref.ID != "attempt-2" || persisted.ID != "attempt-2" || result.Cleanup != CleanupUnknown {
		t.Fatalf("create failure lost durable identity: %+v persisted=%+v", result, persisted)
	}
}
