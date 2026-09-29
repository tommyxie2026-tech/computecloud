package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const controlWriteLeaseTTL = 30 * time.Second

type ControlWriteLease struct {
	JobID       string `json:"job_id"`
	HolderID    string `json:"holder_id"`
	LeaseToken  string `json:"lease_token,omitempty"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

type controlWriteLeaseInput struct {
	HolderID string `json:"holder_id"`
}

func validateLeaseHolder(holder string) error {
	holder = strings.TrimSpace(holder)
	if holder == "" || len(holder) > 128 {
		return status.Error(codes.InvalidArgument, "invalid control lease holder")
	}
	return nil
}

func (s *Server) acquireControlWriteLease(ctx context.Context, jobID, holder string) (*ControlWriteLease, error) {
	principal, err := rpcutil.Require(ctx, "jobs:control", false)
	if err != nil {
		return nil, err
	}
	if err = validateLeaseHolder(holder); err != nil {
		return nil, err
	}
	j, err := s.jobAuthorized(ctx, jobID, "jobs:read")
	if err != nil {
		return nil, err
	}
	if j.owner != principal.Identity.Owner {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}
	token := store.ID()
	now := store.Now()
	expires := now + controlWriteLeaseTTL.Milliseconds()
	err = s.db.Tx(ctx, func(q store.Query) error {
		var oldPrincipal, oldHolder string
		var oldExpiry int64
		e := q.QueryRowContext(ctx, "SELECT principal_id,holder_id,expires_at FROM control_write_leases WHERE job_id=?", jobID).
			Scan(&oldPrincipal, &oldHolder, &oldExpiry)
		if e == nil && oldExpiry > now && (oldPrincipal != principal.Identity.Owner || oldHolder != holder) {
			return status.Error(codes.AlreadyExists, "WRITE_LEASE_HELD")
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = q.ExecContext(ctx,
			"INSERT INTO control_write_leases(job_id,principal_id,holder_id,token_hash,expires_at,updated) VALUES(?,?,?,?,?,?) "+
				"ON CONFLICT(job_id) DO UPDATE SET principal_id=excluded.principal_id,holder_id=excluded.holder_id,token_hash=excluded.token_hash,expires_at=excluded.expires_at,updated=excluded.updated",
			jobID, principal.Identity.Owner, holder, store.Hash([]byte(token)), expires, now)
		return e
	})
	if err != nil {
		return nil, dbErr(err)
	}
	return &ControlWriteLease{JobID: jobID, HolderID: holder, LeaseToken: token, ExpiresAtMS: expires}, nil
}

func (s *Server) renewControlWriteLease(ctx context.Context, jobID, holder, token string) (*ControlWriteLease, error) {
	principal, err := rpcutil.Require(ctx, "jobs:control", false)
	if err != nil {
		return nil, err
	}
	if err = validateLeaseHolder(holder); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, status.Error(codes.FailedPrecondition, "WRITE_LEASE_REQUIRED")
	}
	now := store.Now()
	expires := now + controlWriteLeaseTTL.Milliseconds()
	res, err := s.db.SQL.ExecContext(ctx,
		"UPDATE control_write_leases SET expires_at=?,updated=? WHERE job_id=? AND principal_id=? AND holder_id=? AND token_hash=? AND expires_at>?",
		expires, now, jobID, principal.Identity.Owner, holder, store.Hash([]byte(token)), now)
	if err != nil {
		return nil, dbErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, dbErr(err)
	}
	if n != 1 {
		return nil, status.Error(codes.FailedPrecondition, "WRITE_LEASE_INVALID")
	}
	return &ControlWriteLease{JobID: jobID, HolderID: holder, LeaseToken: token, ExpiresAtMS: expires}, nil
}

func (s *Server) releaseControlWriteLease(ctx context.Context, jobID, holder, token string) error {
	principal, err := rpcutil.Require(ctx, "jobs:control", false)
	if err != nil {
		return err
	}
	if err = validateLeaseHolder(holder); err != nil {
		return err
	}
	if token == "" {
		return status.Error(codes.FailedPrecondition, "WRITE_LEASE_REQUIRED")
	}
	res, err := s.db.SQL.ExecContext(ctx,
		"DELETE FROM control_write_leases WHERE job_id=? AND principal_id=? AND holder_id=? AND token_hash=?",
		jobID, principal.Identity.Owner, holder, store.Hash([]byte(token)))
	if err != nil {
		return dbErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return dbErr(err)
	}
	if n != 1 {
		return status.Error(codes.FailedPrecondition, "WRITE_LEASE_INVALID")
	}
	return nil
}

func (s *Server) requireControlWriteLease(r *http.Request, jobID string) error {
	principal, err := rpcutil.Require(r.Context(), "jobs:control", false)
	if err != nil {
		return err
	}
	values := r.Header.Values("X-Control-Lease")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return status.Error(codes.FailedPrecondition, "WRITE_LEASE_REQUIRED")
	}
	var n int
	err = s.db.SQL.QueryRowContext(r.Context(),
		"SELECT count(*) FROM control_write_leases WHERE job_id=? AND principal_id=? AND token_hash=? AND expires_at>?",
		jobID, principal.Identity.Owner, store.Hash([]byte(values[0])), store.Now()).Scan(&n)
	if err != nil {
		return dbErr(err)
	}
	if n != 1 {
		return status.Error(codes.FailedPrecondition, "WRITE_LEASE_INVALID")
	}
	return nil
}

func readControlLeaseInput(w http.ResponseWriter, r *http.Request) (controlWriteLeaseInput, error) {
	var in controlWriteLeaseInput
	b, err := readJSONBody(w, r, 4096)
	if err != nil {
		return in, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&in); err != nil {
		return in, status.Error(codes.InvalidArgument, "invalid control lease JSON")
	}
	return in, nil
}

func (s *Server) httpAcquireControlWriteLease(w http.ResponseWriter, r *http.Request) {
	in, err := readControlLeaseInput(w, r)
	if err != nil {
		httpError(w, err)
		return
	}
	lease, err := s.acquireControlWriteLease(r.Context(), r.PathValue("id"), in.HolderID)
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, lease)
}

func (s *Server) httpRenewControlWriteLease(w http.ResponseWriter, r *http.Request) {
	in, err := readControlLeaseInput(w, r)
	if err != nil {
		httpError(w, err)
		return
	}
	lease, err := s.renewControlWriteLease(r.Context(), r.PathValue("id"), in.HolderID, r.Header.Get("X-Control-Lease"))
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, lease)
}

func (s *Server) httpReleaseControlWriteLease(w http.ResponseWriter, r *http.Request) {
	in, err := readControlLeaseInput(w, r)
	if err != nil {
		httpError(w, err)
		return
	}
	if err = s.releaseControlWriteLease(r.Context(), r.PathValue("id"), in.HolderID, r.Header.Get("X-Control-Lease")); err != nil {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}


func (s *Server) httpControlCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.requireControlWriteLease(r, r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	b, err := readJSONBody(w, r, 4096)
	if err != nil {
		httpError(w, err)
		return
	}
	var in CancelJobRequest
	if err = json.Unmarshal(b, &in); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid cancellation JSON"))
		return
	}
	j, err := s.CancelJob(r.Context(), r.PathValue("id"), in)
	if err != nil {
		httpError(w, err)
		return
	}
	code := http.StatusAccepted
	if terminal(j.State) || j.Existing {
		code = http.StatusOK
	}
	jsonResponse(w, code, j)
}
