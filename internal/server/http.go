package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func jsonResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func httpError(w http.ResponseWriter, e error) {
	code := http.StatusInternalServerError
	switch status.Code(e) {
	case codes.InvalidArgument, codes.OutOfRange:
		code = 400
	case codes.Unauthenticated:
		code = 401
	case codes.PermissionDenied:
		code = 403
	case codes.NotFound:
		code = 404
	case codes.AlreadyExists:
		code = 409
	case codes.FailedPrecondition:
		code = 400
		if status.Convert(e).Message() == "JOB_NOT_FINISHED" {
			code = 409
		}
	case codes.ResourceExhausted:
		code = 413
	case codes.Unavailable:
		code = 503
	case codes.DeadlineExceeded:
		code = 504
	case codes.Canceled:
		code = 408
	}
	message := cleanCode(e)
	if code == 500 {
		message = "INTERNAL_ERROR"
	}
	if code == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	jsonResponse(w, code, map[string]any{"error": map[string]any{"code": message, "message": message, "request_id": w.Header().Get("X-Request-ID")}})
}
func readJSONBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || ct != "application/json" {
		return nil, status.Error(codes.InvalidArgument, "application/json required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		var large *http.MaxBytesError
		if errors.As(e, &large) {
			return nil, status.Error(codes.ResourceExhausted, "REQUEST_TOO_LARGE")
		}
		return nil, status.Error(codes.InvalidArgument, "invalid request body")
	}
	return b, nil
}
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		b, e := readJSONBody(w, r, s.cfg.Jobs.MaxRequestBytes)
		if e != nil {
			httpError(w, e)
			return
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 {
			httpError(w, status.Error(codes.InvalidArgument, "Idempotency-Key required"))
			return
		}
		j, e := s.SubmitJob(r.Context(), r.Header.Get("Idempotency-Key"), b)
		if e != nil {
			httpError(w, e)
			return
		}
		code := 202
		if j.Existing {
			code = 200
		}
		w.Header().Set("Location", j.Links["self"])
		jsonResponse(w, code, j)
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j, e := s.GetJob(r.Context(), r.PathValue("id"))
		if e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, j)
	})
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		b, e := readJSONBody(w, r, 4096)
		if e != nil {
			httpError(w, e)
			return
		}
		var in CancelJobRequest
		if e = jsonutil.Decode(b, &in); e != nil {
			httpError(w, status.Error(codes.InvalidArgument, "invalid cancellation JSON"))
			return
		}
		j, e := s.CancelJob(r.Context(), r.PathValue("id"), in)
		if e != nil {
			httpError(w, e)
			return
		}
		code := 202
		if terminal(j.State) || j.Existing {
			code = 200
		}
		jsonResponse(w, code, j)
	})
	mux.HandleFunc("GET /v1/jobs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		limit, e := pageLimit(r, 100, 500)
		if e != nil {
			httpError(w, e)
			return
		}
		after, e := decimal(r.URL.Query().Get("after_seq"))
		if e != nil {
			httpError(w, e)
			return
		}
		v, e := s.JobEvents(r.Context(), r.PathValue("id"), after, limit)
		if e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, v)
	})
	mux.HandleFunc("GET /v1/jobs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.JobResult(r.Context(), r.PathValue("id"))
		if e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, v)
	})
	mux.HandleFunc("GET /v1/jobs/{id}/tasks", s.httpJobTasks)
	mux.HandleFunc("GET /v1/jobs/{id}/artifacts", s.httpJobArtifacts)
	mux.HandleFunc("GET /v1/jobs/{id}/artifacts/{artifact}", s.httpJobArtifact)
	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if _, e := rpcutil.User(r.Context()); e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, map[string]any{"api_version": "v0.3", "jobs_enabled": s.cfg.Jobs.Enabled, "mcp_enabled": s.cfg.MCP.Enabled, "model_gateway_enabled": s.cfg.ModelGateway.Enabled, "job_modes": []string{"single", "map_reduce"}, "max_partitions": s.cfg.Jobs.MaxPartitions, "max_parallelism": s.cfg.Jobs.MaxParallelism, "recommended_parallelism": s.cfg.Jobs.RecommendedParallelism, "max_attempts_per_task": 1})
	})
	if s.cfg.MCP.Enabled {
		mux.Handle("/mcp", s.mcpHandler())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", store.ID())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(r.Header.Values("Origin")) > 1 {
			httpError(w, status.Error(codes.PermissionDenied, "invalid Origin"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !containsOrigin(s.cfg.HTTP.AllowedOrigins, origin) {
			httpError(w, status.Error(codes.PermissionDenied, "Origin not allowed"))
			return
		}
		if r.URL.Path == "/v1/models" || strings.HasPrefix(r.URL.Path, "/v1/responses") {
			if s.modelHandler == nil {
				modelError(w, status.Error(codes.Unavailable, "MODEL_GATEWAY_DISABLED"))
				return
			}
			s.modelHandler.ServeHTTP(w, r)
			return
		}
		if len(r.Header.Values("Authorization")) != 1 {
			httpError(w, status.Error(codes.Unauthenticated, "authentication required"))
			return
		}
		ctx, e := s.auth.Bearer(r.Context(), r.Header.Get("Authorization"))
		if e != nil {
			httpError(w, e)
			return
		}
		if _, e = rpcutil.User(ctx); e != nil {
			httpError(w, e)
			return
		}
		timeout := 10 * time.Second
		if strings.Contains(r.URL.Path, "/artifacts/") {
			timeout = 5 * time.Minute
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Second))
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout))
		r = r.WithContext(ctx)
		if r.URL.Path == "/mcp" && r.Method == "POST" {
			b, e := readJSONBody(w, r, s.cfg.Jobs.MaxRequestBytes+4096)
			if e != nil {
				httpError(w, e)
				return
			}
			if e = jsonutil.Validate(b); e != nil {
				httpError(w, status.Error(codes.InvalidArgument, "invalid JSON-RPC document"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		mux.ServeHTTP(w, r)
	})
}
func containsOrigin(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func decimal(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	v, e := strconv.ParseInt(s, 10, 64)
	if e != nil || v < 0 {
		return 0, status.Error(codes.InvalidArgument, "invalid cursor")
	}
	return v, nil
}
func pageLimit(r *http.Request, def, max int) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return def, nil
	}
	v, e := strconv.Atoi(s)
	if e != nil || v < 1 || v > max {
		return 0, status.Error(codes.InvalidArgument, "invalid page limit")
	}
	return v, nil
}
func (s *Server) httpJobTasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, e := s.jobAuthorized(r.Context(), id, "jobs:read"); e != nil {
		httpError(w, e)
		return
	}
	limit, e := pageLimit(r, 50, 100)
	if e != nil {
		httpError(w, e)
		return
	}
	rows, e := s.db.SQL.QueryContext(r.Context(), "SELECT id,state,stage,partition_key,attempt,worker,error_code,blocker FROM tasks WHERE job_id=? AND id>? ORDER BY id LIMIT ?", id, r.URL.Query().Get("after"), limit+1)
	if e != nil {
		httpError(w, dbErr(e))
		return
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var id, state, stage, key, attempt, worker, code, blocker string
		if e = rows.Scan(&id, &state, &stage, &key, &attempt, &worker, &code, &blocker); e != nil {
			httpError(w, dbErr(e))
			return
		}
		items = append(items, map[string]string{"task_id": id, "state": state, "stage": stage, "partition_key": key, "attempt_id": attempt, "worker_id": worker, "error_code": code, "scheduling_blocker": blocker})
	}
	if e = rows.Err(); e != nil {
		httpError(w, dbErr(e))
		return
	}
	more := len(items) > limit
	rows.Close() // Release the single SQLite connection before writing to a slow client.
	if more {
		items = items[:limit]
	}
	next := r.URL.Query().Get("after")
	if len(items) > 0 {
		next = items[len(items)-1]["task_id"]
	}
	jsonResponse(w, 200, map[string]any{"tasks": items, "next": next, "has_more": more})
}
func (s *Server) httpJobArtifacts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, e := s.jobAuthorized(r.Context(), id, "jobs:read"); e != nil {
		httpError(w, e)
		return
	}
	limit, e := pageLimit(r, 50, 100)
	if e != nil {
		httpError(w, e)
		return
	}
	rows, e := s.db.SQL.QueryContext(r.Context(), "SELECT a.id,a.task,a.attempt,a.kind,a.hash,a.size FROM artifacts a JOIN tasks t ON a.task=t.id WHERE t.job_id=? AND a.state='ACCEPTED' AND a.id>? ORDER BY a.id LIMIT ?", id, r.URL.Query().Get("after"), limit+1)
	if e != nil {
		httpError(w, dbErr(e))
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var aid, task, attempt, kind, hash string
		var size int64
		if e = rows.Scan(&aid, &task, &attempt, &kind, &hash, &size); e != nil {
			httpError(w, dbErr(e))
			return
		}
		items = append(items, map[string]any{"artifact_id": aid, "task_id": task, "attempt_id": attempt, "kind": kind, "sha256": hash, "size": size, "download": "/v1/jobs/" + id + "/artifacts/" + aid})
	}
	if e = rows.Err(); e != nil {
		httpError(w, dbErr(e))
		return
	}
	more := len(items) > limit
	rows.Close()
	if more {
		items = items[:limit]
	}
	next := r.URL.Query().Get("after")
	if len(items) > 0 {
		next = items[len(items)-1]["artifact_id"].(string)
	}
	jsonResponse(w, 200, map[string]any{"artifacts": items, "next": next, "has_more": more})
}
func (s *Server) httpJobArtifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, e := s.jobAuthorized(r.Context(), id, "jobs:read"); e != nil {
		httpError(w, e)
		return
	}
	var path, hash string
	var size int64
	e := s.db.SQL.QueryRowContext(r.Context(), "SELECT a.path,a.hash,a.size FROM artifacts a JOIN tasks t ON a.task=t.id WHERE t.job_id=? AND a.id=? AND a.state='ACCEPTED'", id, r.PathValue("artifact")).Scan(&path, &hash, &size)
	if e != nil {
		httpError(w, dbErr(e))
		return
	}
	if !artifactID.MatchString(path) {
		httpError(w, status.Error(codes.Internal, "invalid artifact path"))
		return
	}
	f, e := os.Open(filepath.Join(s.cfg.DataDir, "artifacts", path))
	if e != nil {
		httpError(w, status.Error(codes.NotFound, "ARTIFACT_MISSING"))
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() != size {
		httpError(w, status.Error(codes.FailedPrecondition, "ARTIFACT_CORRUPT"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Content-SHA256", hash)
	w.Header().Set("Content-Disposition", `attachment; filename="`+path+`.tar"`)
	_, _ = io.Copy(w, f)
}
func (s *Server) serveHTTP(ctx context.Context, l net.Listener) error {
	h := &http.Server{Handler: s.HTTPHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	h.BaseContext = func(net.Listener) context.Context { return ctx }
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = h.Shutdown(shutdown)
			_ = h.Close()
		case <-done:
		}
	}()
	defer func() { close(done); <-stopped }()
	var e error
	if s.cfg.TLS.InsecureLoopback {
		if !rpcutil.Loopback(l.Addr().String()) {
			return errors.New("HTTP plaintext allowed only on literal loopback")
		}
		e = h.Serve(l)
	} else {
		e = h.ServeTLS(l, s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
	}
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
