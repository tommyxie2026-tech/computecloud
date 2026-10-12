package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
)

type apiKeyFixture struct {
	s            *Server
	h            http.Handler
	managerToken string
}

func newAPIKeyFixture(t *testing.T, scopes []string) *apiKeyFixture {
	t.Helper()
	managerFile := testutil.Token(t, "api-key-manager")
	cfg := config.Server{DataDir: t.TempDir(), TLS: config.TLS{InsecureLoopback: true}, LeaseSeconds: 6, TickMS: 10, MaxArtifactBytes: 1 << 20, MaxProjectTasks: 4,
		Users: []config.Identity{{TokenFile: managerFile, Owner: "alice", Projects: []string{"p"}, Scopes: scopes}}}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	managerToken, err := config.Token(managerFile)
	if err != nil {
		t.Fatal(err)
	}
	return &apiKeyFixture{s: s, h: s.HTTPHandler(), managerToken: managerToken}
}

func (f *apiKeyFixture) request(t *testing.T, method, path, token string, body []byte) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func (f *apiKeyFixture) createBody(projects, scopes []string) []byte {
	b, _ := json.Marshal(map[string]any{"label": "test-client", "projects": projects, "scopes": scopes, "credentials": []string{}, "expires_at_ms": store.Now() + int64(time.Hour/time.Millisecond)})
	return b
}

func TestAPIKeyManagementRequiresManageScope(t *testing.T) {
	f := newAPIKeyFixture(t, []string{"jobs:read"})
	code, _ := f.request(t, http.MethodPost, "/v1/auth/keys", f.managerToken, f.createBody([]string{"p"}, []string{"jobs:read"}))
	if code != http.StatusForbidden {
		t.Fatalf("key management without scope status=%d", code)
	}
}

func TestCreateAPIKeyReturnsPlaintextOnce(t *testing.T) {
	f := newAPIKeyFixture(t, []string{"api_keys:manage", "jobs:read"})
	code, body := f.request(t, http.MethodPost, "/v1/auth/keys", f.managerToken, f.createBody([]string{"p"}, []string{"jobs:read"}))
	if code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", code, body)
	}
	var created struct{ ID, Key, Prefix string }
	if err := json.Unmarshal(body, &created); err != nil || !strings.HasPrefix(created.Key, "cc_live_") || created.ID == "" || created.Prefix == "" {
		t.Fatalf("create response=%s err=%v", body, err)
	}
	code, listed := f.request(t, http.MethodGet, "/v1/auth/keys", f.managerToken, nil)
	if code != http.StatusOK || strings.Contains(string(listed), created.Key) || strings.Contains(string(listed), store.Hash([]byte(created.Key))) {
		t.Fatalf("list status=%d body leaks key material: %s", code, listed)
	}
}

func exerciseAPIKeyAuthAndRevocation(t *testing.T) {
	f := newAPIKeyFixture(t, []string{"api_keys:manage", "jobs:read"})
	code, body := f.request(t, http.MethodPost, "/v1/auth/keys", f.managerToken, f.createBody([]string{"p"}, []string{"jobs:read"}))
	var created struct{ ID, Key string }
	if code != http.StatusCreated || json.Unmarshal(body, &created) != nil {
		t.Fatalf("create status=%d body=%s", code, body)
	}
	code, _ = f.request(t, http.MethodGet, "/v1/capabilities", created.Key, nil)
	if code != http.StatusOK {
		t.Fatalf("managed key did not authenticate: %d", code)
	}
	code, _ = f.request(t, http.MethodDelete, "/v1/auth/keys/"+created.ID, f.managerToken, nil)
	if code != http.StatusNoContent {
		t.Fatalf("revoke status=%d", code)
	}
	code, _ = f.request(t, http.MethodGet, "/v1/capabilities", created.Key, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked key still authenticated: %d", code)
	}
}

func TestAPIKeyAuthenticatesWithoutRestart(t *testing.T) { exerciseAPIKeyAuthAndRevocation(t) }

func TestRevokedAPIKeyRejectedImmediately(t *testing.T) {
	exerciseAPIKeyAuthAndRevocation(t)
}

func TestAPIKeyCannotEscalateProjectsOrScopes(t *testing.T) {
	f := newAPIKeyFixture(t, []string{"api_keys:manage", "jobs:read"})
	for _, request := range [][]string{{"other-project"}, {"p"}} {
		scopes := []string{"jobs:read"}
		if request[0] == "p" {
			scopes = []string{"jobs:cancel"}
		}
		code, _ := f.request(t, http.MethodPost, "/v1/auth/keys", f.managerToken, f.createBody(request, scopes))
		if code != http.StatusForbidden {
			t.Fatalf("escalation request=%v scopes=%v status=%d", request, scopes, code)
		}
	}
}
