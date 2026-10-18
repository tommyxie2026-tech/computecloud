package server

import (
	"bytes"
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testWebAuth(t *testing.T) *webAuth {
	t.Helper()
	a, e := newWebAuth(config.Web{Auth: config.WebAuth{Username: "admin", IdentityOwner: "o", SessionTTLSeconds: 300}}, []config.Identity{{Owner: "o", Projects: []string{"p"}}})
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func loginWeb(t *testing.T, a *webAuth, password string) (*httptest.ResponseRecorder, *http.Cookie, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	r := httptest.NewRequest("POST", "https://control.example/v1/web/auth/login", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://control.example")
	w := httptest.NewRecorder()
	a.login(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	var c *http.Cookie
	if cs := w.Result().Cookies(); len(cs) > 0 {
		c = cs[0]
	}
	csrf, _ := out["csrf_token"].(string)
	return w, c, csrf
}
func TestWebAuthLogin(t *testing.T) {
	a := testWebAuth(t)
	w, _, _ := loginWeb(t, a, "wrong")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w, c, csrf := loginWeb(t, a, "admin")
	if w.Code != 200 || c == nil || csrf == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", c)
	}
}
func TestWebAuthLifecycle(t *testing.T) {
	a := testWebAuth(t)
	_, c, csrf := loginWeb(t, a, "admin")
	r := httptest.NewRequest("GET", "https://control.example/v1/web/auth/session", nil)
	r.AddCookie(c)
	if _, e := a.authenticate(r); e != nil {
		t.Fatal(e)
	}
	other := testWebAuth(t)
	if _, e := other.authenticate(r); e == nil {
		t.Fatal("restart retained session")
	}
	r.Method = "POST"
	r.Header.Set("Origin", "https://control.example")
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	a.logout(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if _, e := a.authenticate(r); e == nil {
		t.Fatal("logout retained session")
	}
	_, c, _ = loginWeb(t, a, "admin")
	r = httptest.NewRequest("GET", "https://control.example", nil)
	r.AddCookie(c)
	a.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, e := a.authenticate(r); e == nil {
		t.Fatal("expired accepted")
	}
}
func TestWebAuthCookieTransport(t *testing.T) {
	a := testWebAuth(t)
	r := httptest.NewRequest("POST", "http://control.example/v1/web/auth/login", bytes.NewBufferString(`{"username":"admin","password":"admin"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://control.example")
	r.Header.Set("X-Forwarded-Proto", "http")
	a.externalOrigin = "https://control.example"
	w := httptest.NewRecorder()
	a.login(w, r)
	if w.Code != 200 || !w.Result().Cookies()[0].Secure {
		t.Fatal(w.Code, "insecure cookie")
	}
}
func TestWebAuthLimits(t *testing.T) {
	a := testWebAuth(t)
	fixed := time.Now()
	a.now = func() time.Time { return fixed }
	for i := 0; i < 6; i++ {
		loginWeb(t, a, "wrong")
	}
	w, _, _ := loginWeb(t, a, "admin")
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
	a = testWebAuth(t)
	a.maxSessions = 1
	loginWeb(t, a, "admin")
	w, _, _ = loginWeb(t, a, "admin")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
func TestWebAuthServerDoesNotRetainConfiguredPassword(t *testing.T) {
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("control"), 0600); err != nil {
		t.Fatal(err)
	}
	h := newJobHarness(t, false, func(c *config.Server) {
		c.Web = config.Web{Enabled: true, AssetsDir: assets, Auth: config.WebAuth{IdentityOwner: "owner", Password: "configured-fixture-password"}}
	})
	if h.s.cfg.Web.Auth.Password != "" {
		t.Fatal("server retained plaintext web password")
	}
}
func TestWebAuthLogoutExpiredReturnsUnauthenticated(t *testing.T) {
	a := testWebAuth(t)
	_, c, csrf := loginWeb(t, a, "admin")
	a.now = func() time.Time { return time.Now().Add(time.Hour) }
	r := httptest.NewRequest("POST", "https://control.example/v1/web/auth/logout", nil)
	r.AddCookie(c)
	r.Header.Set("Origin", "https://control.example")
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	a.logout(w, r)
	if w.Code != 401 {
		t.Fatalf("expired logout status=%d", w.Code)
	}
}
