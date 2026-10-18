package server

import (
	"context"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
)

// Cookie sessions authenticate only the Control product surface. In particular,
// legacy task, model, MCP and key-administration routes are excluded.
var webControlRoutes = func() *http.ServeMux {
	m := http.NewServeMux()
	noop := func(http.ResponseWriter, *http.Request) {}
	for _, p := range []string{
		"GET /v1/control/bootstrap", "GET /v1/capabilities", "GET /v1/jobs", "POST /v1/jobs", "GET /v1/workers",
		"GET /v1/jobs/{id}", "GET /v1/jobs/{id}/trace", "GET /v1/jobs/{id}/tasks", "GET /v1/jobs/{id}/result",
		"GET /v1/jobs/{id}/events", "GET /v1/jobs/{id}/events/stream", "GET /v1/jobs/{id}/sessions", "GET /v1/jobs/{id}/sessions/{session}",
		"GET /v1/jobs/{id}/approvals", "GET /v1/jobs/{id}/artifacts", "GET /v1/jobs/{id}/artifacts/{artifact}", "GET /v1/jobs/{id}/artifacts/{artifact}/review",
		"GET /v1/jobs/{id}/goal", "POST /v1/jobs/{id}/goal/decisions", "POST /v1/jobs/{id}/goal/replans",
		"POST /v1/jobs/{id}/control-lease", "PUT /v1/jobs/{id}/control-lease", "DELETE /v1/jobs/{id}/control-lease",
		"POST /v1/jobs/{id}/control/cancel", "POST /v1/jobs/{id}/retry", "POST /v1/jobs/{id}/sessions/{session}/inputs",
		"POST /v1/jobs/{id}/sessions/{session}/interrupt", "POST /v1/jobs/{id}/sessions/{session}/resume", "POST /v1/jobs/{id}/approvals/{approval}",
	} {
		m.HandleFunc(p, noop)
	}
	return m
}()

func (s *Server) webIdentity(r *http.Request) (context.Context, error) {
	if s.webAuth == nil {
		return r.Context(), status.Error(codes.Unauthenticated, "authentication required")
	}
	_, pattern := webControlRoutes.Handler(r)
	if pattern == "" {
		return r.Context(), status.Error(codes.PermissionDenied, "WEB_ROUTE_NOT_ALLOWED")
	}
	id, err := s.webAuth.authenticate(r)
	if err != nil {
		return r.Context(), status.Error(codes.Unauthenticated, "authentication required")
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if err := s.webAuth.validateWrite(r); err != nil {
			return r.Context(), status.Error(codes.PermissionDenied, "CSRF_INVALID")
		}
	}
	return rpcutil.WithPrincipal(r.Context(), rpcutil.Principal{Identity: id}), nil
}
