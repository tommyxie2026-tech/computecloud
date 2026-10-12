package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const managedKeyMaxLifetime = 90 * 24 * time.Hour

type createAPIKeyRequest struct {
	Label       string   `json:"label"`
	Projects    []string `json:"projects"`
	Scopes      []string `json:"scopes"`
	Credentials []string `json:"credentials"`
	ExpiresAtMS int64    `json:"expires_at_ms"`
}

func (s *Server) resolveAPIKey(ctx context.Context, digest string) (config.Identity, error) {
	key, err := s.db.LookupAPIKeyByDigest(ctx, digest, store.Now())
	if err != nil {
		return config.Identity{}, err
	}
	return config.Identity{Owner: key.Owner, Projects: key.Projects, Scopes: key.Scopes, Credentials: key.Credentials,
		Limits: config.IdentityLimits{MaxActiveJobs: 8, MaxQueuedJobs: 32, MaxModelRequests: 4}}, nil
}

func (s *Server) httpCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	manager, err := rpcutil.Require(r.Context(), "api_keys:manage", false)
	if err != nil {
		httpError(w, err)
		return
	}
	body, err := readJSONBody(w, r, 16<<10)
	if err != nil {
		httpError(w, err)
		return
	}
	var request createAPIKeyRequest
	if err = json.Unmarshal(body, &request); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "INVALID_API_KEY_REQUEST"))
		return
	}
	if request.Label == "" || len(request.Label) > 128 || len(request.Projects) == 0 || len(request.Scopes) == 0 || len(request.Credentials) > 64 {
		httpError(w, status.Error(codes.InvalidArgument, "INVALID_API_KEY_REQUEST"))
		return
	}
	if !subset(request.Projects, manager.Identity.Projects) || !subset(request.Credentials, manager.Identity.Credentials) {
		httpError(w, status.Error(codes.PermissionDenied, "API_KEY_SCOPE_ESCALATION"))
		return
	}
	allowedManagedScopes := []string{"jobs:submit", "jobs:read", "jobs:cancel", "jobs:extend", "jobs:control", "jobs:retry", "tasks:submit", "tasks:read", "tasks:cancel", "goals:approve", "goals:budget", "goals:constraints", "goals:propose"}
	for _, scope := range request.Scopes {
		if !config.Contains(allowedManagedScopes, scope) || !config.Contains(manager.Identity.Scopes, scope) {
			httpError(w, status.Error(codes.PermissionDenied, "API_KEY_SCOPE_ESCALATION"))
			return
		}
	}
	now := store.Now()
	if request.ExpiresAtMS <= now || request.ExpiresAtMS > now+managedKeyMaxLifetime.Milliseconds() {
		httpError(w, status.Error(codes.InvalidArgument, "API_KEY_EXPIRY_OUT_OF_RANGE"))
		return
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		httpError(w, status.Error(codes.Internal, "API_KEY_GENERATION_FAILED"))
		return
	}
	token := "cc_live_" + base64.RawURLEncoding.EncodeToString(secret)
	prefix := token
	if len(prefix) > 20 {
		prefix = prefix[:20]
	}
	key := store.APIKey{ID: store.ID(), Digest: store.Hash([]byte(token)), Prefix: prefix, Owner: manager.Identity.Owner,
		Projects: request.Projects, Scopes: request.Scopes, Credentials: request.Credentials, Label: request.Label,
		CreatedAt: now, ExpiresAt: request.ExpiresAtMS}
	if err = s.db.CreateAPIKey(r.Context(), key); err != nil {
		httpError(w, status.Error(codes.Internal, "API_KEY_STORE_FAILED"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonResponse(w, http.StatusCreated, map[string]any{"id": key.ID, "key": token, "prefix": key.Prefix, "label": key.Label, "expires_at_ms": key.ExpiresAt})
}

func (s *Server) httpListAPIKeys(w http.ResponseWriter, r *http.Request) {
	manager, err := rpcutil.Require(r.Context(), "api_keys:manage", false)
	if err != nil {
		httpError(w, err)
		return
	}
	keys, err := s.db.ListAPIKeys(r.Context(), manager.Identity.Owner)
	if err != nil {
		httpError(w, dbErr(err))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) httpRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	manager, err := rpcutil.Require(r.Context(), "api_keys:manage", false)
	if err != nil {
		httpError(w, err)
		return
	}
	if err = s.db.RevokeAPIKey(r.Context(), manager.Identity.Owner, r.PathValue("id"), store.Now()); err != nil {
		if err == sql.ErrNoRows {
			httpError(w, status.Error(codes.NotFound, "API_KEY_NOT_FOUND"))
		} else {
			httpError(w, dbErr(err))
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func subset(values, allowed []string) bool {
	if len(values) == 0 {
		return true
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || !config.Contains(allowed, value) {
			return false
		}
		seen[value] = true
	}
	return true
}
