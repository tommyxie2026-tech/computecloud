package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type approvalRuntimePayload struct {
	ApprovalID       string         `json:"approval_id"`
	SessionID        string         `json:"session_id,omitempty"`
	Tool             string         `json:"tool"`
	Action           string         `json:"action"`
	RiskClass        string         `json:"risk_class"`
	ArgumentsSummary string         `json:"arguments_summary,omitempty"`
	PolicyContext    map[string]any `json:"policy_context,omitempty"`
	RequestVersion   int64          `json:"request_version"`
	ExpiresAtMS      int64          `json:"expires_at_ms,omitempty"`
}

type sessionRuntimePayload struct {
	SessionRef string `json:"session_ref"`
}

// persistAgentControlRuntimeState turns certified structured Runtime events into
// durable Control-plane state. Runtime events remain evidence; they do not gain
// authority to mutate Job/Attempt generation ownership.
func persistAgentControlRuntimeState(ctx context.Context, q store.Query, event *pb.Event) error {
	if event == nil {
		return nil
	}
	switch event.Type {
	case "session.started", "session.resumed":
		var payload sessionRuntimePayload
		if err := json.Unmarshal(event.PayloadJson, &payload); err != nil || payload.SessionRef == "" {
			return status.Error(codes.InvalidArgument, "invalid runtime session event")
		}
		res, err := q.ExecContext(ctx, `UPDATE attempts SET runtime_session_ref=?
			WHERE id=? AND task=? AND generation=? AND released=0`,
			payload.SessionRef, event.AttemptId, event.TaskId, event.Generation)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return status.Error(codes.FailedPrecondition, control.ErrorAttemptFenced.String())
		}
		return nil
	case "approval.requested":
		return persistApprovalRequested(ctx, q, event)
	default:
		return nil
	}
}

func persistApprovalRequested(ctx context.Context, q store.Query, event *pb.Event) error {
	var payload approvalRuntimePayload
	if err := json.Unmarshal(event.PayloadJson, &payload); err != nil {
		return status.Error(codes.InvalidArgument, "invalid approval request payload")
	}
	if payload.SessionID == "" {
		payload.SessionID = event.AttemptId
	}
	requestedAt := time.Now().UTC()
	var expiresAt time.Time
	if payload.ExpiresAtMS > 0 {
		expiresAt = time.UnixMilli(payload.ExpiresAtMS).UTC()
	}
	model := control.ApprovalRequest{
		ProtocolVersion: control.ProtocolV1Alpha1,
		ApprovalID: payload.ApprovalID,
		TaskID: event.TaskId,
		AttemptID: event.AttemptId,
		Generation: event.Generation,
		SessionID: payload.SessionID,
		Tool: payload.Tool,
		Action: payload.Action,
		RiskClass: payload.RiskClass,
		ArgumentsSummary: payload.ArgumentsSummary,
		PolicyContext: payload.PolicyContext,
		RequestVersion: payload.RequestVersion,
		RequestedAt: requestedAt,
		ExpiresAt: expiresAt,
		State: control.ApprovalPending,
	}
	var jobID sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT job_id FROM tasks WHERE id=?", event.TaskId).Scan(&jobID); err != nil {
		return err
	}
	if !jobID.Valid {
		return status.Error(codes.FailedPrecondition, "approval requires managed Job")
	}
	model.JobID = jobID.String
	if err := model.Validate(); err != nil {
		return status.Error(codes.InvalidArgument, "invalid approval request")
	}
	hash := job.Hash(job.JSON(model))
	var oldHash string
	err := q.QueryRowContext(ctx, `SELECT request_hash FROM approval_requests
		WHERE approval_id=? AND request_version=?`, payload.ApprovalID, payload.RequestVersion).Scan(&oldHash)
	if err == nil {
		if oldHash != hash {
			return status.Error(codes.AlreadyExists, control.ErrorOperationConflict.String())
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var maxVersion int64
	if err = q.QueryRowContext(ctx, "SELECT coalesce(max(request_version),0) FROM approval_requests WHERE approval_id=?", payload.ApprovalID).Scan(&maxVersion); err != nil {
		return err
	}
	if maxVersion > payload.RequestVersion {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	}
	if maxVersion > 0 && payload.RequestVersion > maxVersion {
		if _, err = q.ExecContext(ctx, `UPDATE approval_requests SET state='SUPERSEDED'
			WHERE approval_id=? AND state='PENDING'`, payload.ApprovalID); err != nil {
			return err
		}
	}
	policyJSON := job.JSON(payload.PolicyContext)
	_, err = q.ExecContext(ctx, `INSERT INTO approval_requests(
		approval_id,request_version,job_id,task_id,attempt_id,generation,session_id,
		tool,action,risk_class,arguments_summary,policy_context_json,request_hash,
		requested_at,expires_at,state
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'PENDING')`,
		payload.ApprovalID, payload.RequestVersion, jobID.String, event.TaskId, event.AttemptId,
		event.Generation, payload.SessionID, payload.Tool, payload.Action, payload.RiskClass,
		payload.ArgumentsSummary, policyJSON, hash, requestedAt.UnixMilli(), payload.ExpiresAtMS)
	if err != nil {
		return err
	}
	return appendJobEvent(ctx, q, jobID.String, "approval.requested", map[string]any{
		"protocol_version": control.ProtocolV1Alpha1,
		"approval_id": payload.ApprovalID,
		"request_version": payload.RequestVersion,
		"task_id": event.TaskId,
		"attempt_id": event.AttemptId,
		"generation": event.Generation,
		"risk_class": payload.RiskClass,
	}, "approval-request:"+payload.ApprovalID+":"+fmtInt(payload.RequestVersion), hash)
}

func fmtInt(v int64) string {
	return strconv.FormatInt(v, 10)
}

func expireApprovals(ctx context.Context, q store.Query, jobID string, now int64) error {
	rows, err := q.QueryContext(ctx, `SELECT approval_id,request_version,task_id,attempt_id,generation
		FROM approval_requests WHERE job_id=? AND state='PENDING' AND expires_at>0 AND expires_at<=?
		ORDER BY requested_at`, jobID, now)
	if err != nil {
		return err
	}
	type expired struct {
		id, task, attempt string
		version, generation int64
	}
	var all []expired
	for rows.Next() {
		var item expired
		if err = rows.Scan(&item.id, &item.version, &item.task, &item.attempt, &item.generation); err != nil {
			rows.Close()
			return err
		}
		all = append(all, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range all {
		res, err := q.ExecContext(ctx, `UPDATE approval_requests SET state='EXPIRED',decided_at=?
			WHERE approval_id=? AND request_version=? AND state='PENDING'`, now, item.id, item.version)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			continue
		}
		if err = appendJobEvent(ctx, q, jobID, "approval.expired", map[string]any{
			"protocol_version": control.ProtocolV1Alpha1,
			"approval_id": item.id, "request_version": item.version,
			"task_id": item.task, "attempt_id": item.attempt, "generation": item.generation,
		}, "approval-expired:"+item.id+":"+fmtInt(item.version), "expired"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) JobApprovals(ctx context.Context, jobID string) ([]control.ApprovalRequest, error) {
	if _, err := s.jobAuthorized(ctx, jobID, "jobs:read"); err != nil {
		return nil, err
	}
	if err := s.db.Tx(ctx, func(q store.Query) error {
		return expireApprovals(ctx, q, jobID, store.Now())
	}); err != nil {
		return nil, dbErr(err)
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT approval_id,request_version,task_id,attempt_id,generation,
		session_id,tool,action,risk_class,arguments_summary,policy_context_json,requested_at,expires_at,state
		FROM approval_requests WHERE job_id=? ORDER BY requested_at,approval_id,request_version`, jobID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := []control.ApprovalRequest{}
	for rows.Next() {
		var item control.ApprovalRequest
		var policy []byte
		var requested, expires int64
		var state string
		item.ProtocolVersion = control.ProtocolV1Alpha1
		item.JobID = jobID
		if err = rows.Scan(&item.ApprovalID, &item.RequestVersion, &item.TaskID, &item.AttemptID, &item.Generation,
			&item.SessionID, &item.Tool, &item.Action, &item.RiskClass, &item.ArgumentsSummary,
			&policy, &requested, &expires, &state); err != nil {
			return nil, dbErr(err)
		}
		item.State = control.ApprovalState(state)
		item.RequestedAt = time.UnixMilli(requested).UTC()
		if expires > 0 {
			item.ExpiresAt = time.UnixMilli(expires).UTC()
		}
		if len(policy) > 0 {
			if err = json.Unmarshal(policy, &item.PolicyContext); err != nil {
				return nil, status.Error(codes.Unavailable, "invalid persisted approval policy context")
			}
		}
		if err = item.Validate(); err != nil {
			return nil, status.Error(codes.Unavailable, "invalid persisted approval")
		}
		out = append(out, item)
	}
	return out, dbErr(rows.Err())
}

func (s *Server) httpJobApprovals(w http.ResponseWriter, r *http.Request) {
	items, err := s.JobApprovals(r.Context(), r.PathValue("id"))
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"approvals": items})
}
