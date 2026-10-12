package server

import (
	"context"
	"net"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func rateLimitServer(t *testing.T, maxEntries, idleSeconds int, identities ...string) (*Server, []string) {
	t.Helper()
	paths := make([]string, len(identities))
	tokens := make([]string, len(identities))
	users := make([]string, len(identities))
	for i, name := range identities {
		paths[i] = testutil.Token(t, name)
		b, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = strings.TrimSpace(string(b))
		users[i] = fmt.Sprintf("  - token_file: %s\n    owner: %q\n    projects: [p]\n    scopes: [jobs:read]\n", strconv.Quote(paths[i]), name)
	}
	root := t.TempDir()
	configPath := filepath.Join(root, "server.yaml")
	lines := []string{
		"server:", "  data_dir: " + strconv.Quote(filepath.Join(root, "data")),
		"  lease_seconds: 3", "  tick_ms: 10", "  max_artifact_bytes: 1024", "  max_project_tasks: 8",
		"  tls:", "    insecure_loopback: true", "  http:", "    rate_limit:",
		"      rate_per_minute: 1", "      burst: 1",
		fmt.Sprintf("      max_entries: %d", maxEntries),
		fmt.Sprintf("      idle_timeout_seconds: %d", idleSeconds), "  users:",
		strings.TrimSuffix(strings.Join(users, ""), "\n"),
	}
	if err := os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load rate-limit config: %v", err)
	}
	s, err := New(c.Server)
	if err != nil {
		t.Fatalf("create rate-limit server: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, tokens
}

func rateLimitRequest(s *Server, token, forwardedFor string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	w := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(w, r)
	return w
}

func TestHTTPHandlerRateLimitPerCredential(t *testing.T) {
	s, tokens := rateLimitServer(t, 10, 600, "user-a", "user-b")
	if got := rateLimitRequest(s, tokens[0], "").Code; got != http.StatusOK {
		t.Fatalf("first request status=%d, want 200", got)
	}
	blocked := rateLimitRequest(s, tokens[0], "")
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") != "60" {
		t.Fatalf("second request status=%d retry-after=%q, want 429/60", blocked.Code, blocked.Header().Get("Retry-After"))
	}
	if got := rateLimitRequest(s, tokens[1], "").Code; got != http.StatusOK {
		t.Fatalf("independent credential status=%d, want 200", got)
	}
}

func TestHTTPHandlerRateLimitIgnoresForwardedFor(t *testing.T) {
	s, tokens := rateLimitServer(t, 10, 600, "user-a")
	if got := rateLimitRequest(s, tokens[0], "198.51.100.1").Code; got != http.StatusOK {
		t.Fatalf("first request status=%d, want 200", got)
	}
	if got := rateLimitRequest(s, tokens[0], "203.0.113.2").Code; got != http.StatusTooManyRequests {
		t.Fatalf("spoofed source address bypassed identity limit: status=%d", got)
	}
}

func TestManagedAPIKeyLookupIsRateLimitedByDirectIPBeforeResolver(t *testing.T) {
	s, _ := rateLimitServer(t, 10, 600, "user-a")
	lookups := 0
	s.auth.SetAPIKeyResolver(func(_ context.Context, _ string) (config.Identity, error) {
		lookups++
		return config.Identity{Owner: "api-owner", Projects: []string{"p"}, Scopes: []string{"jobs:read"}}, nil
	})
	call := func(token, forwarded string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
		r.RemoteAddr = "198.51.100.8:44321"
		r.Header.Set("Authorization", "Bearer "+token)
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		w := httptest.NewRecorder()
		s.HTTPHandler().ServeHTTP(w, r)
		return w
	}
	if got := call("cc_live_first", "192.0.2.1").Code; got != http.StatusOK {
		t.Fatalf("first managed-key request status=%d, want 200", got)
	}
	blocked := call("cc_live_second", "203.0.113.9")
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("second managed-key request status=%d retry-after=%q, want 429 with Retry-After", blocked.Code, blocked.Header().Get("Retry-After"))
	}
	if lookups != 1 {
		t.Fatalf("API-key resolver called %d times, want 1; forwarded headers must not change direct-IP bucket", lookups)
	}
}

func TestGRPCManagedAPIKeyLookupIsRateLimitedBeforeResolver(t *testing.T) {
	s, _ := rateLimitServer(t, 10, 600, "user-a")
	lookups := 0
	s.auth.SetAPIKeyResolver(func(_ context.Context, _ string) (config.Identity, error) {
		lookups++
		return config.Identity{Owner: "api-owner", Projects: []string{"p"}, Scopes: []string{"jobs:read"}}, nil
	})
	call := func(token string) error {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
		ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("198.51.100.8"), Port: 44321}})
		_, err := s.authUnary(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return nil, nil })
		return err
	}
	if err := call("cc_live_first"); err != nil {
		t.Fatalf("first managed-key gRPC request returned %v, want success", err)
	}
	if err := call("cc_live_second"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second managed-key gRPC request returned %v, want ResourceExhausted", err)
	}
	if lookups != 1 {
		t.Fatalf("gRPC API-key resolver called %d times, want 1", lookups)
	}
}

func TestHTTPHandlerRateLimitEvictsIdleCredential(t *testing.T) {
	s, tokens := rateLimitServer(t, 1, 1, "user-a")
	if got := rateLimitRequest(s, tokens[0], "").Code; got != http.StatusOK {
		t.Fatalf("first request status=%d, want 200", got)
	}
	if got := rateLimitRequest(s, tokens[0], "").Code; got != http.StatusTooManyRequests {
		t.Fatalf("immediate repeat status=%d, want 429", got)
	}
	time.Sleep(1100 * time.Millisecond)
	if got := rateLimitRequest(s, tokens[0], "").Code; got != http.StatusOK {
		t.Fatalf("request after idle eviction status=%d, want 200", got)
	}
}

func TestRequestLimiterBoundsEntries(t *testing.T) {
	s, tokens := rateLimitServer(t, 1, 600, "user-a", "user-b")
	_ = rateLimitRequest(s, tokens[0], "")
	_ = rateLimitRequest(s, tokens[1], "")
	field := reflect.ValueOf(s).Elem().FieldByName("requestLimiter")
	if !field.IsValid() || field.IsNil() {
		t.Fatal("Server requestLimiter is missing")
	}
	entries := field.Elem().FieldByName("entries")
	if !entries.IsValid() || entries.Kind() != reflect.Map {
		t.Fatal("requestLimiter entries map is missing")
	}
	if entries.Len() > 1 {
		t.Fatalf("limiter retained %d credential entries with max_entries=1", entries.Len())
	}
}

func TestHTTPHandlerRateLimitConcurrentRequests(t *testing.T) {
	s, tokens := rateLimitServer(t, 10, 600, "user-a")
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed, limited := 0, 0
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch rateLimitRequest(s, tokens[0], "").Code {
			case http.StatusOK:
				mu.Lock()
				allowed++
				mu.Unlock()
			case http.StatusTooManyRequests:
				mu.Lock()
				limited++
				mu.Unlock()
			default:
				t.Error("unexpected response from rate-limited handler")
			}
		}()
	}
	wg.Wait()
	if allowed != 1 || limited != 31 {
		t.Fatalf("concurrent requests allowed=%d limited=%d, want 1/31", allowed, limited)
	}
}

func TestModelGatewayUsesHTTPRateLimit(t *testing.T) {
	tokenFile := testutil.Token(t, "model-user")
	token, err := config.Token(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	upstreamKey := testutil.Token(t, "upstream-key")
	s, err := New(config.Server{
		DataDir: t.TempDir(), TLS: config.TLS{InsecureLoopback: true},
		LeaseSeconds: 3, TickMS: 10, MaxArtifactBytes: 1024, MaxProjectTasks: 4,
		HTTP:  config.HTTP{RateLimit: config.RequestLimit{RatePerMinute: 1, Burst: 1, MaxEntries: 10, IdleTimeoutSeconds: 600}},
		Users: []config.Identity{{TokenFile: tokenFile, Owner: "model-owner", Projects: []string{"p"}, Scopes: []string{"models:invoke"}, ModelProject: "p", ModelRoute: "route"}},
		ModelGateway: config.ModelGateway{Enabled: true, Routes: map[string]config.ModelRoute{
			"route": {BaseURL: "https://model.example.invalid/v1", APIKeyFile: upstreamKey, AllowedModels: []string{"model"}, MaxInflight: 1},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.HTTPHandler().ServeHTTP(w, r)
		return w
	}
	if got := call().Code; got != http.StatusOK {
		t.Fatalf("first model request status=%d, want 200", got)
	}
	if got := call().Code; got != http.StatusTooManyRequests {
		t.Fatalf("second model request status=%d, want 429", got)
	}
}
