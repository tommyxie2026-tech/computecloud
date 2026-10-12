package server

import (
	"crypto/sha256"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

type requestBucket struct {
	tokens  float64
	updated time.Time
}

type requestLimitEntry struct {
	global   requestBucket
	classes  map[string]requestBucket
	lastSeen time.Time
}

type requestLimiter struct {
	mu      sync.Mutex
	policy  config.RequestLimit
	entries map[[32]byte]*requestLimitEntry
}

func newRequestLimiter(policy config.RequestLimit) *requestLimiter {
	return &requestLimiter{policy: policy, entries: make(map[[32]byte]*requestLimitEntry)}
}

func (l *requestLimiter) allow(tokenHash [32]byte, class string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	idle := time.Duration(l.policy.IdleTimeoutSeconds) * time.Second
	for key, entry := range l.entries {
		if now.Sub(entry.lastSeen) >= idle {
			delete(l.entries, key)
		}
	}
	entry := l.entries[tokenHash]
	if entry == nil {
		if len(l.entries) >= l.policy.MaxEntries {
			l.evictOldest()
		}
		entry = &requestLimitEntry{
			global:  requestBucket{tokens: float64(l.policy.Burst), updated: now},
			classes: make(map[string]requestBucket),
		}
		l.entries[tokenHash] = entry
	}
	entry.lastSeen = now
	classBucket, ok := entry.classes[class]
	if !ok {
		classBucket = requestBucket{tokens: float64(l.policy.Burst), updated: now}
	}
	globalTokens := refill(entry.global, l.policy, now)
	classTokens := refill(classBucket, l.policy, now)
	if globalTokens < 1 || classTokens < 1 {
		entry.global = requestBucket{tokens: globalTokens, updated: now}
		entry.classes[class] = requestBucket{tokens: classTokens, updated: now}
		return false, max(retryAfterSeconds(globalTokens, l.policy), retryAfterSeconds(classTokens, l.policy))
	}
	entry.global = requestBucket{tokens: globalTokens - 1, updated: now}
	entry.classes[class] = requestBucket{tokens: classTokens - 1, updated: now}
	return true, 1
}

func refill(bucket requestBucket, policy config.RequestLimit, now time.Time) float64 {
	if now.Before(bucket.updated) {
		return bucket.tokens
	}
	perSecond := float64(policy.RatePerMinute) / 60
	tokens := bucket.tokens + now.Sub(bucket.updated).Seconds()*perSecond
	if tokens > float64(policy.Burst) {
		tokens = float64(policy.Burst)
	}
	return tokens
}

func (l *requestLimiter) evictOldest() {
	if len(l.entries) == 0 {
		return
	}
	keys := make([][32]byte, 0, len(l.entries))
	for key := range l.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := l.entries[keys[i]].lastSeen, l.entries[keys[j]].lastSeen
		if left.Equal(right) {
			return strings.Compare(string(keys[i][:]), string(keys[j][:])) < 0
		}
		return left.Before(right)
	})
	delete(l.entries, keys[0])
}

func (s *Server) allowRequest(authorization, class string, now time.Time) (bool, int) {
	if s.requestLimiter == nil {
		return true, 1
	}
	return s.requestLimiter.allow(sha256.Sum256([]byte(authorization)), class, now)
}

func publicRequestClass(r *http.Request) string {
	if r.URL.Path == "/v1/models" || strings.HasPrefix(r.URL.Path, "/v1/responses") {
		return "model"
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/jobs" {
		return "job_submit"
	}
	if strings.HasSuffix(r.URL.Path, "/events/stream") {
		return "stream"
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		return "write"
	}
	return "read"
}

func writeRateLimited(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	jsonResponse(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{
		"code": "RATE_LIMITED", "message": "RATE_LIMITED", "request_id": w.Header().Get("X-Request-ID"),
	}})
}

func (s *Server) rateLimitHTTP(w http.ResponseWriter, r *http.Request, authorization string) bool {
	allowed, retryAfter := s.allowRequest(authorization, publicRequestClass(r), time.Now())
	if allowed {
		return true
	}
	writeRateLimited(w, retryAfter)
	return false
}

// rateLimitAPIKeyLookup protects the SQLite-backed managed-key resolver before
// authentication succeeds. It uses only the socket peer address: forwarding
// headers are untrusted on this listener and must not be able to rotate buckets.
func (s *Server) rateLimitAPIKeyLookup(w http.ResponseWriter, r *http.Request, authorization string) bool {
	if !strings.HasPrefix(authorization, "Bearer cc_live_") || s.apiKeyLookupLimiter == nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		host = ip.String()
	}
	key := sha256.Sum256([]byte("managed-api-key-lookup:" + host))
	allowed, retryAfter := s.apiKeyLookupLimiter.allow(key, "auth_lookup", time.Now())
	if allowed {
		return true
	}
	writeRateLimited(w, retryAfter)
	return false
}

func retryAfterSeconds(tokens float64, policy config.RequestLimit) int {
	if tokens >= 1 {
		return 1
	}
	seconds := math.Ceil((1 - tokens) * 60 / float64(policy.RatePerMinute))
	if seconds < 1 {
		return 1
	}
	return int(seconds)
}
