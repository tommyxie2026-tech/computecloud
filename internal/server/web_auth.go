package server

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

const webCookie = "cc_web_session"

type webSession struct {
	csrf    string
	expires time.Time
	ctx     context.Context
	cancel  context.CancelFunc
}
type webAuth struct {
	mu               sync.Mutex
	sessions         map[[32]byte]*webSession
	identity         config.Identity
	username         string
	salt             []byte
	passwordHash     []byte
	ttl              time.Duration
	now              func() time.Time
	limiter          *requestLimiter
	checks           chan struct{}
	maxSessions      int
	insecureLoopback bool
	externalOrigin   string
}

func newWebAuth(cfg config.Web, users []config.Identity) (*webAuth, error) {
	password, err := config.WebPassword(cfg.Auth)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 32)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return nil, err
	}
	a := &webAuth{sessions: make(map[[32]byte]*webSession), username: cfg.Auth.Username, salt: salt, passwordHash: derived, ttl: time.Duration(cfg.Auth.SessionTTLSeconds) * time.Second, now: time.Now, checks: make(chan struct{}, 2), maxSessions: 1024, externalOrigin: cfg.ExternalOrigin, limiter: newRequestLimiter(config.RequestLimit{RatePerMinute: 10, Burst: 6, MaxEntries: 1024, IdleTimeoutSeconds: 600})}
	for _, u := range users {
		if u.Owner == cfg.Auth.IdentityOwner {
			a.identity = u
		}
	}
	if password == "admin" {
		slog.Warn("Control Web default password is active; configure a unique password before public deployment")
	}
	return a, nil
}
func randomWebToken() string { return base64.RawURLEncoding.EncodeToString(mustWebRandom()) }
func mustWebRandom() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func (a *webAuth) validOrigin(r *http.Request, required bool) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return !required
	}
	if len(values) != 1 {
		return false
	}
	origin := values[0]
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	expected := a.externalOrigin
	if expected == "" {
		scheme := "https"
		if a.insecureLoopback {
			scheme = "http"
		}
		expected = scheme + "://" + r.Host
	}
	return origin == expected
}
func (a *webAuth) cookie(r *http.Request, token string, maxAge int) *http.Cookie {
	secure := true
	if a.insecureLoopback && r.TLS == nil && a.externalOrigin == "" {
		secure = false
	}
	return &http.Cookie{Name: webCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge}
}
func webFailure(w http.ResponseWriter, code int, message string) {
	jsonResponse(w, code, map[string]any{"error": map[string]string{"code": message}})
}
func (a *webAuth) login(w http.ResponseWriter, r *http.Request) {
	if !a.validOrigin(r, true) {
		webFailure(w, 403, "ORIGIN_NOT_ALLOWED")
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ok, retry := a.limiter.allow(sha256.Sum256([]byte(host)), "login", a.now()); !ok {
		w.Header().Set("Retry-After", fmtWebInt(retry))
		webFailure(w, 429, "LOGIN_RATE_LIMITED")
		return
	}
	select {
	case a.checks <- struct{}{}:
		defer func() { <-a.checks }()
	default:
		webFailure(w, 429, "LOGIN_RATE_LIMITED")
		return
	}
	body, err := readJSONBody(w, r, 4096)
	if err != nil {
		webFailure(w, 400, "INVALID_LOGIN")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.Unmarshal(body, &input) != nil {
		webFailure(w, 400, "INVALID_LOGIN")
		return
	}
	hash, err := pbkdf2.Key(sha256.New, input.Password, a.salt, 600000, 32)
	if err != nil || subtle.ConstantTimeCompare(hash, a.passwordHash) != 1 || subtle.ConstantTimeCompare([]byte(input.Username), []byte(a.username)) != 1 {
		webFailure(w, 401, "AUTHENTICATION_FAILED")
		return
	}
	token := randomWebToken()
	now := a.now()
	expires := now.Add(a.ttl)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	session := &webSession{csrf: randomWebToken(), expires: expires, ctx: ctx, cancel: cancel}
	a.mu.Lock()
	for k, v := range a.sessions {
		if !now.Before(v.expires) {
			v.cancel()
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= a.maxSessions {
		a.mu.Unlock()
		cancel()
		webFailure(w, 503, "SESSION_CAPACITY_REACHED")
		return
	}
	a.sessions[sha256.Sum256([]byte(token))] = session
	a.mu.Unlock()
	http.SetCookie(w, a.cookie(r, token, int(a.ttl/time.Second)))
	a.writeSession(w, session)
}
func (a *webAuth) sessionFor(r *http.Request) (*webSession, error) {
	cookies := r.CookiesNamed(webCookie)
	if len(cookies) != 1 || len(cookies[0].Value) != 43 {
		return nil, errors.New("authentication required")
	}
	key := sha256.Sum256([]byte(cookies[0].Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[key]
	if s == nil {
		return nil, errors.New("authentication required")
	}
	if !a.now().Before(s.expires) || s.ctx.Err() != nil {
		s.cancel()
		delete(a.sessions, key)
		return nil, errors.New("authentication required")
	}
	return s, nil
}
func (a *webAuth) authenticate(r *http.Request) (config.Identity, error) {
	_, err := a.sessionFor(r)
	return a.identity, err
}
func (a *webAuth) validateWrite(r *http.Request) error {
	s, err := a.sessionFor(r)
	if err != nil {
		return err
	}
	if !a.validOrigin(r, true) || len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrf)) != 1 {
		return errors.New("CSRF validation failed")
	}
	return nil
}
func (a *webAuth) writeSession(w http.ResponseWriter, s *webSession) {
	jsonResponse(w, 200, map[string]any{"username": a.username, "expires_at_ms": s.expires.UnixMilli(), "csrf_token": s.csrf})
}
func (a *webAuth) session(w http.ResponseWriter, r *http.Request) {
	s, err := a.sessionFor(r)
	if err != nil {
		webFailure(w, 401, "AUTHENTICATION_REQUIRED")
		return
	}
	a.writeSession(w, s)
}
func (a *webAuth) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.validateWrite(r); err != nil {
		webFailure(w, 403, "CSRF_INVALID")
		return
	}
	cookie, _ := r.Cookie(webCookie)
	a.mu.Lock()
	key := sha256.Sum256([]byte(cookie.Value))
	if s := a.sessions[key]; s != nil {
		s.cancel()
		delete(a.sessions, key)
	}
	a.mu.Unlock()
	http.SetCookie(w, a.cookie(r, "", -1))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}
func (a *webAuth) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, s := range a.sessions {
		s.cancel()
		delete(a.sessions, k)
	}
}
func fmtWebInt(n int) string {
	if n <= 0 {
		return "1"
	}
	return strconv.Itoa(n)
}
