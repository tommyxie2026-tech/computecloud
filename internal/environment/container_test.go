package environment

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestContainerProviderLifecycleAndUnknownCleanup(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cc-env-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "runtime.sock")
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("test-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var cleanupKnown atomic.Bool
	cleanupKnown.Store(true)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"profile":"codex_http","isolation":"container"}`))
		case "/v1/environments/attempt-1":
			if r.Method == http.MethodDelete && !cleanupKnown.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if r.Method == http.MethodDelete {
				_, _ = w.Write([]byte(`{"state":"RELEASED","cleanup":"CONFIRMED"}`))
				return
			}
			_, _ = w.Write([]byte(`{"state":"ACTIVE","cleanup":"PENDING"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	p := containerProvider{}
	recorded := Ref{}
	prepared, err := p.Prepare(context.Background(), PrepareRequest{
		AttemptID: "attempt-1", TaskID: "task-1", Generation: 1, CWD: dir,
		RuntimeProfile: "codex_http", RuntimeEndpoint: "unix://" + socket, RuntimeTokenFile: tokenFile,
	}, func(ref Ref) error { recorded = ref; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if recorded != prepared.Ref {
		t.Fatalf("reference not durably recorded: %+v", prepared.Ref)
	}
	inspection, err := p.Inspect(context.Background(), recorded)
	if err != nil || inspection.State != StateActive || inspection.Cleanup != CleanupPending {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	cleanupKnown.Store(false)
	failed, err := p.Release(context.Background(), recorded)
	if err == nil || failed.Cleanup != CleanupUnknown {
		t.Fatalf("unknown cleanup accepted: %+v err=%v", failed, err)
	}
	cleanupKnown.Store(true)
	released, err := p.Release(context.Background(), recorded)
	if err != nil || released.Cleanup != CleanupConfirmed {
		t.Fatalf("release=%+v err=%v", released, err)
	}
}

func TestContainerProviderRejectsHostCLIAndMalformedRef(t *testing.T) {
	p := containerProvider{}
	if _, err := p.Prepare(context.Background(), PrepareRequest{
		AttemptID: "a", TaskID: "t", Generation: 1, CWD: "/tmp", RuntimeProfile: "codex_exec",
	}, func(Ref) error { t.Fatal("host CLI reference recorded"); return nil }); err == nil {
		t.Fatal("host CLI accepted as container environment")
	}
	if _, err := p.Inspect(context.Background(), Ref{Provider: "container", ID: "../escape", Endpoint: "unix:///tmp/x", TokenFile: "/tmp/token"}); err == nil {
		t.Fatal("malformed reference accepted")
	}
}
