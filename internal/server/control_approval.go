package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
	RequestedAtMS    int64          `json:"requested_at_ms,omitempty"`
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
			WHERE id=? AND task=? AND generation=? AND released=0
			  AND (runtime_session_ref='' OR runtime_session_ref=?)`,
			payload.SessionRef, event.AttemptId, event.TaskId, event.Generation, payload.SessionRef)
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
	if payload.RequestedAtMS > 0 {
		requestedAt = time.UnixMilli(payload.RequestedAtMS).UTC()
	}
	var expiresAt time.Time
	if payload.ExpiresAtMS > 0 {
		expiresAt = time.UnixMilli(payload.ExpiresAtMS).UTC()
		if payload.RequestedAtMS == 0 && !expiresAt.After(requestedAt) {
			requestedAt = expiresAt.Add(-time.Millisecond)
		}
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
	hash := job.Hash(job.JSON(map[string]any{
		"approval_id": model.ApprovalID,
		"job_id": model.JobID,
		"task_id": model.TaskID,
		"attempt_id": model.AttemptID,
		"generation": model.Generation,
		"session_id": model.SessionID,
		"tool": model.Tool,
		"action": model.Action,
		"risk_class": model.RiskClass,
		"arguments_summary": model.ArgumentsSummary,
		"policy_context": model.PolicyContext,
		"request_version": model.RequestVersion,
		"requested_at_ms": payload.RequestedAtMS,
		"expires_at_ms": payload.ExpiresAtMS,
	}))
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
	if (maxVersion == 0 && payload.RequestVersion != 1) ||
		(maxVersion > 0 && payload.RequestVersion != maxVersion+1) {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	}
	if maxVersion > 0 {
		res, updateErr := q.ExecContext(ctx, `UPDATE approval_requests SET state='SUPERSEDED',decided_at=?
			WHERE approval_id=? AND state='PENDING'`, store.Now(), payload.ApprovalID)
		if updateErr != nil {
			return updateErr
		}
		changed, updateErr := res.RowsAffected()
		if updateErr != nil {
			return updateErr
		}
		if changed > 0 {
			if updateErr = appendJobEvent(ctx, q, jobID.String, "approval.superseded", map[string]any{
				"protocol_version": control.ProtocolV1Alpha1,
				"approval_id": payload.ApprovalID,
				"request_version": maxVersion,
				"superseded_by": payload.RequestVersion,
				"task_id": event.TaskId,
				"attempt_id": event.AttemptId,
				"generation": event.Generation,
			}, "approval-superseded:"+payload.ApprovalID+":"+fmtInt(maxVersion), store.Hash([]byte(fmtInt(payload.RequestVersion)))); updateErr != nil {
				return updateErr
			}
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


type approvalDecisionPayload struct {
	ApprovalID     string `json:"approval_id"`
	RequestVersion int64  `json:"request_version"`
	Decision       string `json:"decision"`
}

type resumeControlPayload struct {
	SessionRef string `json:"session_ref"`
}

func validateApprovalDecisionControl(ctx context.Context, q store.Query, jobID string, in ControlOperationRequest, payload approvalDecisionPayload) error {
	if in.ResourceID != payload.ApprovalID || payload.ApprovalID == "" || payload.RequestVersion < 1 || (payload.Decision != "accept" && payload.Decision != "reject") {
		return status.Error(codes.InvalidArgument, "invalid approval decision payload")
	}
	if err := expireApprovals(ctx, q, jobID, store.Now()); err != nil {
		return err
	}
	var taskID, attemptID, state, lockedOperation string
	var generation int64
	err := q.QueryRowContext(ctx, `SELECT task_id,attempt_id,generation,state,decision_operation_id
		FROM approval_requests WHERE approval_id=? AND request_version=? AND job_id=?`,
		payload.ApprovalID, payload.RequestVersion, jobID).
		Scan(&taskID, &attemptID, &generation, &state, &lockedOperation)
	if errors.Is(err, sql.ErrNoRows) {
		var maxVersion int64
		if e := q.QueryRowContext(ctx, "SELECT coalesce(max(request_version),0) FROM approval_requests WHERE approval_id=? AND job_id=?",
			payload.ApprovalID, jobID).Scan(&maxVersion); e != nil {
			return e
		}
		if maxVersion != 0 {
			return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
		}
		return status.Error(codes.NotFound, "NOT_FOUND")
	}
	if err != nil {
		return err
	}
	if taskID != in.TaskID || attemptID != in.ExpectedAttemptID || generation != in.ExpectedGeneration {
		return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
	}
	if state != "PENDING" {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	}
	if lockedOperation != "" && lockedOperation != in.OperationID {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	}
	return nil
}

func validateResumeControl(ctx context.Context, q store.Query, jobID string, in ControlOperationRequest, payload resumeControlPayload) error {
	if in.ResourceID != in.ExpectedAttemptID || payload.SessionRef == "" {
		return status.Error(codes.InvalidArgument, "explicit session_ref required")
	}
	var taskState, sessionRef string
	err := q.QueryRowContext(ctx, `SELECT t.state,a.runtime_session_ref
		FROM tasks t JOIN attempts a ON a.id=t.attempt
		WHERE t.id=? AND t.job_id=? AND a.id=? AND a.generation=? AND a.released=0`,
		in.TaskID, jobID, in.ExpectedAttemptID, in.ExpectedGeneration).Scan(&taskState, &sessionRef)
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
	}
	if err != nil {
		return err
	}
	if taskState != "RUNNING" && taskState != "STARTING" {
		return status.Error(codes.FailedPrecondition, control.ErrorExecutionUnverifiable.String())
	}
	if sessionRef == "" || sessionRef != payload.SessionRef {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	}
	return nil
}

func applyApprovalControlResult(ctx context.Context, q store.Query, cmd *pb.ControlCommand, ack *pb.CommandAck) error {
	if cmd == nil || ack == nil {
		return status.Error(codes.InvalidArgument, "approval result identity required")
	}
	var state, lockedOperation string
	err := q.QueryRowContext(ctx, `SELECT state,decision_operation_id FROM approval_requests
		WHERE approval_id=? AND request_version=? AND attempt_id=? AND generation=?`,
		cmd.ApprovalId, cmd.RequestVersion, cmd.AttemptId, cmd.Generation).Scan(&state, &lockedOperation)
	if err != nil {
		return err
	}
	switch ack.State {
	case "COMPLETED":
		next := "ACCEPTED"
		if cmd.Decision == "reject" {
			next = "REJECTED"
		}
		if state == next && lockedOperation == ack.OperationId {
			return nil
		}
		if state != "PENDING" || lockedOperation != ack.OperationId {
			return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
		}
		_, err = q.ExecContext(ctx, `UPDATE approval_requests SET state=?,decided_at=?,actor=?,decision_operation_id=?
			WHERE approval_id=? AND request_version=? AND state='PENDING' AND decision_operation_id=?`,
			next, store.Now(), cmd.PrincipalId, ack.OperationId,
			cmd.ApprovalId, cmd.RequestVersion, ack.OperationId)
		return err
	case "UNKNOWN":
		if state == "SUPERSEDED" && lockedOperation == ack.OperationId {
			return nil
		}
		if state != "PENDING" || lockedOperation != ack.OperationId {
			return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
		}
		_, err = q.ExecContext(ctx, `UPDATE approval_requests SET state='SUPERSEDED',decided_at=?
			WHERE approval_id=? AND request_version=? AND state='PENDING' AND decision_operation_id=?`,
			store.Now(), cmd.ApprovalId, cmd.RequestVersion, ack.OperationId)
		return err
	case "REJECTED":
		if state == "PENDING" && lockedOperation == "" {
			return nil
		}
		if state != "PENDING" || lockedOperation != ack.OperationId {
			return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
		}
		_, err = q.ExecContext(ctx, `UPDATE approval_requests SET actor='',decision_operation_id=''
			WHERE approval_id=? AND request_version=? AND state='PENDING' AND decision_operation_id=?`,
			cmd.ApprovalId, cmd.RequestVersion, ack.OperationId)
		return err
	default:
		return status.Error(codes.InvalidArgument, "invalid approval result state")
	}
}

func expireApprovals(ctx context.Context, q store.Query, jobID string, now int64) error {
	rows, err := q.QueryContext(ctx, `SELECT approval_id,request_version,task_id,attempt_id,generation
		FROM approval_requests WHERE job_id=? AND state='PENDING' AND decision_operation_id='' AND expires_at>0 AND expires_at<=?
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
