package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type gatewayRoute struct {
	config config.ModelRoute
	key    string
	slots  chan struct{}
}
type modelGateway struct {
	s      *Server
	client *http.Client
	slots  chan struct{}
	routes map[string]*gatewayRoute
}
type modelIdentity struct {
	owner, project, job, attempt, route, model string
	deadline                                   int64
}

func newModelGateway(s *Server) (*modelGateway, error) {
	g := s.cfg.ModelGateway
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: time.Duration(g.ConnectTimeoutSeconds) * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: time.Duration(g.ConnectTimeoutSeconds) * time.Second, ResponseHeaderTimeout: time.Duration(g.HeaderTimeoutSeconds) * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: g.MaxInflight, MaxIdleConnsPerHost: g.MaxInflight, MaxConnsPerHost: g.MaxInflight, MaxResponseHeaderBytes: 64 << 10, DisableCompression: true}
	out := &modelGateway{s: s, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, slots: make(chan struct{}, g.MaxInflight), routes: map[string]*gatewayRoute{}}
	for name, r := range g.Routes {
		b, e := os.ReadFile(r.APIKeyFile)
		if e != nil {
			return nil, errors.New("gateway upstream key unavailable")
		}
		key := strings.TrimSpace(string(b))
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return nil, errors.New("invalid gateway upstream key")
		}
		out.routes[name] = &gatewayRoute{r, key, make(chan struct{}, r.MaxInflight)}
	}
	// This process owns the data-directory lock: no surviving request can settle these rows.
	_, e := s.db.SQL.Exec("UPDATE gateway_requests SET state='UNKNOWN',usage_complete=0,finished=?,error_code='SERVER_RESTARTED' WHERE state='STARTED'", store.Now())
	return out, e
}
func modelAttemptToken(a *pb.Assignment) string {
	return "ccma_" + job.Hash(job.JSON([]string{"computecloud-model-token-v1", a.AttemptId, strconv.FormatInt(a.Generation, 10), a.LeaseToken}))
}
func (s *Server) bindGateway(ctx context.Context, q store.Query, a *pb.Assignment, j *Job) error {
	if j == nil {
		return nil
	}
	route := j.frozen.Routes[a.Spec.CredentialRef]
	if route == "" {
		return nil
	}
	r := s.cfg.ModelGateway.Routes[route]
	if !s.cfg.ModelGateway.Enabled || r.BaseURL == "" || j.frozen.RouteDigests[route] != config.RouteDigest(r) {
		return status.Error(codes.FailedPrecondition, "GATEWAY_ROUTE_CHANGED")
	}
	a.Gateway = &pb.GatewayAccess{BaseUrl: s.cfg.ModelGateway.PublicBaseURL, Route: route}
	_, e := q.ExecContext(ctx, "UPDATE attempts SET model_token_hash=? WHERE id=?", job.Hash([]byte(modelAttemptToken(a))), a.AttemptId)
	return e
}
func (g *modelGateway) attemptIdentity(ctx context.Context, q store.Query, hash string) (modelIdentity, error) {
	var id modelIdentity
	var raw []byte
	var until, deadline int64
	var released bool
	var state, jobState, reason string
	e := q.QueryRowContext(ctx, `SELECT t.owner,t.project,t.job_id,a.id,json_extract(t.spec,'$.model'),a.lease_until,a.released,t.state,j.state,j.stop_reason,j.deadline,j.spec FROM attempts a JOIN tasks t ON t.attempt=a.id JOIN jobs j ON j.id=t.job_id WHERE a.model_token_hash=?`, hash).Scan(&id.owner, &id.project, &id.job, &id.attempt, &id.model, &until, &released, &state, &jobState, &reason, &deadline, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return id, status.Error(codes.Unauthenticated, "INVALID_MODEL_TOKEN")
	}
	if e != nil {
		return id, e
	}
	if released || until <= store.Now() || deadline <= store.Now() || (state != "STARTING" && state != "RUNNING" && state != "VERIFYING") || terminal(jobState) || jobState == "STOPPING" || jobState == "RECONCILING" || reason != "" {
		return id, status.Error(codes.PermissionDenied, "ATTEMPT_NOT_ACTIVE")
	}
	var frozen job.Frozen
	if e = json.Unmarshal(raw, &frozen); e != nil {
		return id, e
	}
	var cred string
	if e = q.QueryRowContext(ctx, "SELECT json_extract(spec,'$.credential_ref') FROM tasks WHERE attempt=?", id.attempt).Scan(&cred); e != nil {
		return id, e
	}
	id.route = frozen.Routes[cred]
	r := g.routes[id.route]
	if r == nil || frozen.RouteDigests[id.route] != config.RouteDigest(r.config) {
		return id, status.Error(codes.PermissionDenied, "GATEWAY_ROUTE_CHANGED")
	}
	id.deadline = deadline
	return id, nil
}
func (g *modelGateway) identity(ctx context.Context, header string) (modelIdentity, string, error) {
	var id modelIdentity
	if !strings.HasPrefix(header, "Bearer ") {
		return id, "", status.Error(codes.Unauthenticated, "authentication required")
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if strings.HasPrefix(token, "ccma_") {
		hash := job.Hash([]byte(token))
		id, e := g.attemptIdentity(ctx, g.s.db.SQL, hash)
		return id, hash, e
	}
	c, e := g.s.auth.Bearer(ctx, header)
	if e != nil {
		return id, "", e
	}
	p, ok := rpcutil.PrincipalFrom(c)
	if !ok || p.Worker || !config.Contains(p.Identity.Scopes, "models:invoke") {
		return id, "", status.Error(codes.PermissionDenied, "models:invoke required")
	}
	id.owner = p.Identity.Owner
	id.project = p.Identity.ModelProject
	id.route = p.Identity.ModelRoute
	if g.routes[id.route] == nil {
		return id, "", status.Error(codes.PermissionDenied, "MODEL_ROUTE_UNAVAILABLE")
	}
	return id, "", nil
}
func (g *modelGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "" {
		modelError(w, status.Error(codes.InvalidArgument, "WEBSOCKET_UNSUPPORTED"))
		return
	}
	if r.URL.RawQuery != "" || (r.URL.Path != "/v1/models" && r.URL.Path != "/v1/responses" && r.URL.Path != "/v1/responses/compact") {
		modelError(w, status.Error(codes.NotFound, "UNSUPPORTED_MODEL_ENDPOINT"))
		return
	}
	if len(r.Header.Values("Authorization")) != 1 {
		modelError(w, status.Error(codes.Unauthenticated, "authentication required"))
		return
	}
	id, hash, e := g.identity(r.Context(), r.Header.Get("Authorization"))
	if e != nil {
		modelError(w, dbErr(e))
		return
	}
	route := g.routes[id.route]
	if r.URL.Path == "/v1/models" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		data := []map[string]any{}
		for _, m := range route.config.AllowedModels {
			if id.model == "" || id.model == m {
				data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "configured-upstream"})
			}
		}
		jsonResponse(w, 200, map[string]any{"object": "list", "data": data})
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		gatewayBusy(w)
		return
	}
	select {
	case route.slots <- struct{}{}:
		defer func() { <-route.slots }()
	default:
		gatewayBusy(w)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
	body, e := readJSONBody(w, r, g.s.cfg.ModelGateway.MaxRequestBytes)
	if e != nil {
		modelError(w, e)
		return
	}
	if e = jsonutil.Validate(body); e != nil {
		modelError(w, status.Error(codes.InvalidArgument, "INVALID_MODEL_JSON"))
		return
	}
	var fields map[string]json.RawMessage
	if e = json.Unmarshal(body, &fields); e != nil || fields == nil {
		modelError(w, status.Error(codes.InvalidArgument, "MODEL_OBJECT_REQUIRED"))
		return
	}
	if e = validateModelBody(fields, r.URL.Path == "/v1/responses/compact"); e != nil {
		modelError(w, status.Error(codes.InvalidArgument, e.Error()))
		return
	}
	var model string
	if e = json.Unmarshal(fields["model"], &model); e != nil || !config.Contains(route.config.AllowedModels, model) || (id.model != "" && model != id.model) {
		modelError(w, status.Error(codes.PermissionDenied, "MODEL_NOT_ALLOWED"))
		return
	}
	for _, k := range []string{"previous_response_id", "conversation"} {
		if v, ok := fields[k]; ok && string(v) != "null" {
			modelError(w, status.Error(codes.InvalidArgument, "SERVER_SIDE_CONTEXT_UNSUPPORTED"))
			return
		}
	}
	for _, k := range []string{"store", "background"} {
		if v, ok := fields[k]; ok && string(v) != "false" {
			modelError(w, status.Error(codes.InvalidArgument, "STORE_AND_BACKGROUND_MUST_BE_FALSE"))
			return
		}
	}
	if r.URL.Path == "/v1/responses" {
		fields["store"] = json.RawMessage("false")
		body = job.JSON(fields)
	}
	var stream bool
	if v, ok := fields["stream"]; ok {
		if e = json.Unmarshal(v, &stream); e != nil {
			modelError(w, status.Error(codes.InvalidArgument, "INVALID_STREAM_FLAG"))
			return
		}
	}
	if r.URL.Path == "/v1/responses/compact" && stream {
		modelError(w, status.Error(codes.InvalidArgument, "COMPACTION_IS_NOT_STREAMING"))
		return
	}
	deadline := time.Now().Add(time.Duration(g.s.cfg.ModelGateway.RequestTimeoutSeconds) * time.Second)
	if id.deadline > 0 && time.UnixMilli(id.deadline).Before(deadline) {
		deadline = time.UnixMilli(id.deadline)
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	rid := store.ID()
	w.Header().Set("X-Request-ID", rid)
	e = g.s.db.Tx(ctx, func(q store.Query) error {
		if hash != "" {
			current, e := g.attemptIdentity(ctx, q, hash)
			if e != nil {
				return e
			}
			id = current
		}
		var jid, aid any
		if id.job != "" {
			jid = id.job
			aid = id.attempt
		}
		_, e := q.ExecContext(ctx, "INSERT INTO gateway_requests(id,owner,project,job_id,attempt_id,route,model,endpoint,state,started) VALUES(?,?,?,?,?,?,?,?,'STARTED',?)", rid, id.owner, id.project, jid, aid, id.route, model, r.URL.Path, store.Now())
		return e
	})
	if e != nil {
		modelError(w, dbErr(e))
		return
	}
	outcome := gatewayOutcome{state: "UNKNOWN", code: "UPSTREAM_INTERRUPTED"}
	defer func() { g.record(rid, outcome) }()
	done := make(chan struct{})
	defer close(done)
	if hash != "" {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-done:
					return
				case <-ticker.C:
					if _, e := g.attemptIdentity(ctx, g.s.db.SQL, hash); e != nil {
						cancel()
						return
					}
				}
			}
		}()
	}
	endpoint := strings.TrimPrefix(r.URL.Path, "/v1")
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(route.config.BaseURL, "/")+endpoint, bytes.NewReader(body))
	if e != nil {
		outcome.code = "UPSTREAM_REQUEST_INVALID"
		modelError(w, status.Error(codes.Internal, "UPSTREAM_REQUEST_INVALID"))
		return
	}
	req.Header.Set("Authorization", "Bearer "+route.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Request-ID", rid)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	for _, name := range []string{"OpenAI-Beta", "X-Codex-Turn-Metadata"} {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	res, e := g.client.Do(req)
	if e != nil {
		modelError(w, status.Error(codes.Unavailable, "UPSTREAM_UNAVAILABLE"))
		return
	}
	defer res.Body.Close()
	outcome.upstreamID = res.Header.Get("X-Request-ID")
	if encoding := res.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		outcome.code = "UNSUPPORTED_UPSTREAM_ENCODING"
		modelError(w, status.Error(codes.Unavailable, outcome.code))
		return
	}
	for name, values := range res.Header {
		low := strings.ToLower(name)
		if low == "content-type" || low == "retry-after" || strings.HasPrefix(low, "x-ratelimit-") {
			w.Header()[name] = values
		}
	}
	if outcome.upstreamID != "" {
		w.Header().Set("X-Upstream-Request-ID", outcome.upstreamID)
	}
	w.Header().Set("Cache-Control", "no-store")
	idle := time.Duration(g.s.cfg.ModelGateway.StreamIdleTimeoutSeconds) * time.Second
	timer := time.AfterFunc(idle, cancel)
	defer timer.Stop()
	if res.StatusCode >= 200 && res.StatusCode < 300 && strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		w.WriteHeader(res.StatusCode)
		observer := &sseUsage{out: &outcome}
		buf := make([]byte, 16<<10)
		for {
			n, re := res.Body.Read(buf)
			if n > 0 {
				timer.Reset(idle)
				if e = observer.feed(buf[:n]); e != nil {
					outcome.state = "UNKNOWN"
					outcome.code = "SSE_EVENT_LIMIT"
					return
				}
				_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
				if _, e = w.Write(buf[:n]); e != nil {
					outcome.state = "UNKNOWN"
					outcome.code = "DOWNSTREAM_DISCONNECTED"
					return
				}
				if e = http.NewResponseController(w).Flush(); e != nil {
					outcome.state = "UNKNOWN"
					outcome.code = "DOWNSTREAM_DISCONNECTED"
					return
				}
			}
			if re != nil {
				if re != io.EOF {
					outcome.state = "UNKNOWN"
					outcome.code = "UPSTREAM_INTERRUPTED"
				}
				return
			}
		}
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 16<<20+1))
	if e != nil || len(data) > 16<<20 {
		outcome.code = "UPSTREAM_RESPONSE_LIMIT_OR_INTERRUPTED"
		modelError(w, status.Error(codes.Unavailable, outcome.code))
		return
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		outcome.state = "COMPLETE"
		outcome.code = ""
		outcome.readUsage(data)
		var native struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &native) != nil {
			outcome.state = "UNKNOWN"
			outcome.code = "INVALID_UPSTREAM_JSON"
		} else if native.Status == "incomplete" || native.Status == "failed" {
			outcome.state = "FAILED"
			outcome.code = "RESPONSE_" + strings.ToUpper(native.Status)
		}
	} else {
		outcome.state = "FAILED"
		outcome.code = "UPSTREAM_HTTP_" + strconv.Itoa(res.StatusCode)
		outcome.readUsage(data)
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.WriteHeader(res.StatusCode)
	if _, e = w.Write(data); e != nil {
		outcome.state = "UNKNOWN"
		outcome.code = "DOWNSTREAM_DISCONNECTED"
	}
}
func gatewayBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	jsonResponse(w, 429, map[string]any{"error": map[string]any{"code": "GATEWAY_BUSY", "message": "gateway concurrency limit reached", "type": "rate_limit_error", "param": nil}})
}
func (g *modelGateway) record(id string, o gatewayOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, e := g.s.db.SQL.ExecContext(ctx, "UPDATE gateway_requests SET state=?,upstream_request_id=?,input_tokens=?,output_tokens=?,usage_json=?,usage_complete=?,finished=?,error_code=? WHERE id=? AND state='STARTED'", o.state, o.upstreamID, o.input, o.output, o.usage, o.known, store.Now(), o.code, id)
	if e != nil {
		slog.Error("gateway usage settlement failed", "request_id", id, "error", e)
	}
}
