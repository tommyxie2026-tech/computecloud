package server

import (
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func webRequest(s *Server, method, path string, c *http.Cookie, csrf, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://control.example"+path, strings.NewReader(`{}`))
	if c != nil {
		r.AddCookie(c)
	}
	r.Header.Set("Origin", "https://control.example")
	r.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if bearer != "" {
		r.Header.Set("Authorization", bearer)
	}
	w := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(w, r)
	return w
}
func TestWebAuthorizationBearerPrecedence(t *testing.T) {
	h := newJobHarness(t, false)
	a := testWebAuth(t)
	a.identity = h.s.cfg.Users[0]
	h.s.webAuth = a
	_, c, csrf := loginWeb(t, a, "admin")
	if w := webRequest(h.s, "GET", "/v1/control/bootstrap", c, csrf, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := webRequest(h.s, "GET", "/v1/control/bootstrap", c, csrf, "Bearer invalid"); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestWebAuthorizationRouteBoundary(t *testing.T) {
	h := newJobHarness(t, false)
	a := testWebAuth(t)
	a.identity = h.s.cfg.Users[0]
	h.s.webAuth = a
	_, c, csrf := loginWeb(t, a, "admin")
	for _, p := range []string{"/v1/auth/keys", "/v1/models", "/agent/v1/messages", "/mcp"} {
		w := webRequest(h.s, "POST", p, c, csrf, "")
		if w.Code != 401 && w.Code != 403 && w.Code != 503 {
			t.Fatal(p, w.Code)
		}
	}
}
func TestWebAuthorizationCSRF(t *testing.T) {
	h := newJobHarness(t, false)
	a := testWebAuth(t)
	a.identity = h.s.cfg.Users[0]
	h.s.webAuth = a
	_, c, csrf := loginWeb(t, a, "admin")
	for _, token := range []string{"", "wrong"} {
		if w := webRequest(h.s, "POST", "/v1/jobs", c, token, ""); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "https://control.example/v1/jobs", strings.NewReader(`{}`))
	r.AddCookie(c)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	h.s.HTTPHandler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestWebAuthorizationScopes(t *testing.T) {
	h := newJobHarness(t, false)
	b, _ := json.Marshal(h.spec("single"))
	j, e := h.s.SubmitJob(h.ctx, "web-owner", b)
	if e != nil {
		t.Fatal(e)
	}
	a := testWebAuth(t)
	a.identity = h.s.cfg.Users[1]
	h.s.webAuth = a
	_, c, csrf := loginWeb(t, a, "admin")
	w := webRequest(h.s, "GET", "/v1/jobs/"+j.ID, c, csrf, "")
	if w.Code != 404 && w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestWebAuthorizationSessionCancel(t *testing.T) {
	a := testWebAuth(t)
	_, c, _ := loginWeb(t, a, "admin")
	r := httptest.NewRequest("GET", "https://control.example", nil)
	r.AddCookie(c)
	ss, e := a.sessionFor(r)
	if e != nil {
		t.Fatal(e)
	}
	a.close()
	select {
	case <-ss.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("not canceled")
	}
}

var _ = config.Web{}

type webStreamRecorder struct {
	*httptest.ResponseRecorder
	started chan struct{}
	once    sync.Once
}

func (w *webStreamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.once.Do(func() { close(w.started) })
}
func TestWebAuthorizationLogoutStopsActiveStream(t *testing.T) {
	h := newJobHarness(t, false)
	b, _ := json.Marshal(h.spec("single"))
	j, e := h.s.SubmitJob(h.ctx, "stream-revoke", b)
	if e != nil {
		t.Fatal(e)
	}
	a := testWebAuth(t)
	a.identity = h.s.cfg.Users[0]
	h.s.webAuth = a
	_, c, csrf := loginWeb(t, a, "admin")
	r := httptest.NewRequest("GET", "https://control.example/v1/jobs/"+j.ID+"/events/stream?after_seq=0", nil)
	r.AddCookie(c)
	w := &webStreamRecorder{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{})}
	done := make(chan struct{})
	go func() { h.s.HTTPHandler().ServeHTTP(w, r); close(done) }()
	select {
	case <-w.started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	out := webRequest(h.s, "POST", "/v1/web/auth/logout", c, csrf, "")
	if out.Code != 204 {
		t.Fatal(out.Code)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked session retained active stream")
	}
}
