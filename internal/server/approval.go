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

type runtimeApprovalPayload struct {
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

type approvalDecisionPayload struct {
	ApprovalID     string `json:"approval_id"`
	RequestVersion int64  `json:"request_version"`
	Decision       string `json:"decision"`
}

func persistApprovalRequested(ctx context.Context, q store.Query, event *pb.Event) error {
	if event == nil || event.Type != "approval.requested" {
		return nil
	}
	var payload runtimeApprovalPayload
	if err := json.Unmarshal(event.PayloadJson, &payload); err != nil {
		return status.Error(codes.InvalidArgument, "invalid approval request payload")
	}
	if payload.SessionID == "" {
		payload.SessionID = event.AttemptId
	}
	now := store.Now()
	requestedMS := payload.RequestedAtMS
	if requestedMS <= 0 {
		requestedMS = now
	}
	requested := time.UnixMilli(requestedMS).UTC()
	var expires time.Time
	if payload.ExpiresAtMS > 0 {
		expires = time.UnixMilli(payload.ExpiresAtMS).UTC()
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
		RequestedAt: requested,
		ExpiresAt: expires,
		State: control.ApprovalPending,
	}
	var jobID sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT job_id FROM tasks WHERE id=?", event.TaskId).Scan(&jobID); err != nil {
		return err
	}
	if !jobID.Valid || jobID.String == "" {
		return status.Error(codes.FailedPrecondition, "approval requires managed job")
	}
	model.JobID = jobID.String
	if err := model.Validate(); err != nil {
		return status.Error(codes.InvalidArgument, "invalid approval request payload")
	}
	rawPolicy := job.JSON(payload.PolicyContext)
	hash := store.Hash(job.JSON(payload))

	var oldHash, oldState string
	err := q.QueryRowContext(ctx,
		"SELECT request_hash,state FROM approval_requests WHERE approval_id=? AND request_version=?",
		payload.ApprovalID, payload.RequestVersion).Scan(&oldHash, &oldState)
	if err == nil {
		if oldHash != hash {
			return status.Error(codes.AlreadyExists, "APPROVAL_VERSION_CONFLICT")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = q.ExecContext(ctx, `UPDATE approval_requests
		SET state='SUPERSEDED'
		WHERE approval_id=? AND state='PENDING' AND request_version<?`,
		payload.ApprovalID, payload.RequestVersion); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO approval_requests(
		approval_id,request_version,job_id,task_id,attempt_id,generation,session_id,
		tool,action,risk_class,arguments_summary,policy_context,request_hash,
		requested_at,expires_at,state
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		payload.ApprovalID, payload.RequestVersion, jobID.String, event.TaskId,
		event.AttemptId, event.Generation, payload.SessionID,
		payload.Tool, payload.Action, payload.RiskClass, payload.ArgumentsSummary,
		rawPolicy, hash, requestedMS, payload.ExpiresAtMS, "PENDING")
	return err
}

func expireJobApprovals(ctx context.Context, q store.Query, jobID string) error {
	now := store.Now()
	rows, err := q.QueryContext(ctx, `SELECT approval_id,request_version,task_id,attempt_id,generation
		FROM approval_requests
		WHERE job_id=? AND state='PENDING' AND decision='' AND expires_at>0 AND expires_at<=?`, jobID, now)
	if err != nil {
		return err
	}
	type expired struct {
		id, task, attempt string
		version, generation int64
	}
	var items []expired
	for rows.Next() {
		var x expired
		if err = rows.Scan(&x.id, &x.version, &x.task, &x.attempt, &x.generation); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, x := range items {
		res, updateErr := q.ExecContext(ctx, `UPDATE approval_requests SET state='EXPIRED'
			WHERE approval_id=? AND request_version=? AND state='PENDING'`, x.id, x.version)
		if updateErr != nil {
			return updateErr
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			if err = appendJobEvent(ctx, q, jobID, "approval.expired", map[string]any{
				"protocol_version": control.ProtocolV1Alpha1,
				"approval_id": x.id, "request_version": x.version,
				"task_id": x.task, "attempt_id": x.attempt, "generation": x.generation,
			}, "approval-expired:"+x.id+":"+strconv.FormatInt(x.version, 10), ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) JobApprovals(ctx context.Context, jobID string) ([]control.ApprovalRequest, error) {
	if _, err := s.jobAuthorized(ctx, jobID, "jobs:read"); err != nil {
		return nil, err
	}
	out := []control.ApprovalRequest{}
	err := s.db.Tx(ctx, func(q store.Query) error {
		if err := expireJobApprovals(ctx, q, jobID); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT approval_id,request_version,task_id,attempt_id,generation,
			session_id,tool,action,risk_class,arguments_summary,policy_context,requested_at,expires_at,state
			FROM approval_requests WHERE job_id=?
			ORDER BY requested_at,approval_id,request_version`, jobID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a control.ApprovalRequest
			var policy []byte
			var requested, expires int64
			var state string
			if err = rows.Scan(&a.ApprovalID, &a.RequestVersion, &a.TaskID, &a.AttemptID, &a.Generation,
				&a.SessionID, &a.Tool, &a.Action, &a.RiskClass, &a.ArgumentsSummary, &policy,
				&requested, &expires, &state); err != nil {
				return err
			}
			a.ProtocolVersion = control.ProtocolV1Alpha1
			a.JobID = jobID
			a.State = control.ApprovalState(state)
			a.RequestedAt = time.UnixMilli(requested).UTC()
			if expires > 0 {
				a.ExpiresAt = time.UnixMilli(expires).UTC()
			}
			if len(policy) > 0 {
				if err = json.Unmarshal(policy, &a.PolicyContext); err != nil {
					return err
				}
			}
			if err = a.Validate(); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, dbErr(err)
	}
	return out, nil
}

func validateApprovalDecision(ctx context.Context, q store.Query, jobID string, in ControlOperationRequest, payload approvalDecisionPayload) error {
	if payload.ApprovalID == "" || payload.RequestVersion < 1 ||
		(payload.Decision != "ACCEPT" && payload.Decision != "REJECT") {
		return status.Error(codes.InvalidArgument, "invalid approval decision")
	}
	if in.ResourceType != "approval" || in.ResourceID != payload.ApprovalID {
		return status.Error(codes.InvalidArgument, "approval resource mismatch")
	}
	if err := expireJobApprovals(ctx, q, jobID); err != nil {
		return err
	}
	var taskID, attemptID, state, existingDecision string
	var generation int64
	if err := q.QueryRowContext(ctx, `SELECT task_id,attempt_id,generation,state,decision
		FROM approval_requests WHERE approval_id=? AND request_version=? AND job_id=?`,
		payload.ApprovalID, payload.RequestVersion, jobID).
		Scan(&taskID, &attemptID, &generation, &state, &existingDecision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "NOT_FOUND")
		}
		return err
	}
	if taskID != in.TaskID || attemptID != in.ExpectedAttemptID || generation != in.ExpectedGeneration {
		return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
	}
	switch state {
	case "PENDING":
		if existingDecision != "" {
			return status.Error(codes.AlreadyExists, "APPROVAL_DECISION_IN_FLIGHT")
		}
		return nil
	case "EXPIRED", "SUPERSEDED":
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	case "ACCEPTED", "REJECTED":
		return status.Error(codes.AlreadyExists, "APPROVAL_ALREADY_DECIDED")
	default:
		return status.Error(codes.FailedPrecondition, control.ErrorExecutionUnverifiable.String())
	}
}

func applyApprovalControlResult(ctx context.Context, q store.Query, cmd *pb.ControlCommand, ack *pb.CommandAck) error {
	if cmd == nil || cmd.Action != "approval" || cmd.ApprovalId == "" || cmd.RequestVersion < 1 {
		return nil
	}
	if ack.State != "COMPLETED" {
		return nil
	}
	state := "REJECTED"
	if cmd.Decision == "ACCEPT" {
		state = "ACCEPTED"
	}
	res, err := q.ExecContext(ctx, `UPDATE approval_requests
		SET state=?,decision=?,decided_by=?,decided_at=?
		WHERE approval_id=? AND request_version=? AND attempt_id=? AND generation=? AND state='PENDING'`,
		state, cmd.Decision, cmd.PrincipalId, store.Now(),
		cmd.ApprovalId, cmd.RequestVersion, cmd.AttemptId, cmd.Generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	}
	return nil
}

func (s *Server) httpJobApprovals(w http.ResponseWriter, r *http.Request) {
	items, err := s.JobApprovals(r.Context(), r.PathValue("id"))
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"approvals": items})
}

func (s *Server) httpApprovalDecision(w http.ResponseWriter, r *http.Request) {
	b, err := readJSONBody(w, r, 16<<10)
	if err != nil {
		httpError(w, err)
		return
	}
	var in struct {
		OperationID        string `json:"operation_id"`
		TaskID             string `json:"task_id"`
		ExpectedAttemptID  string `json:"expected_attempt_id"`
		ExpectedGeneration int64  `json:"expected_generation"`
		ExpectedResourceVersion int64 `json:"expected_resource_version"`
		RequestVersion     int64  `json:"request_version"`
		Decision           string `json:"decision"`
	}
	if err = json.Unmarshal(b, &in); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid approval decision JSON"))
		return
	}
	approvalID := r.PathValue("approval")
	payload := approvalDecisionPayload{ApprovalID: approvalID, RequestVersion: in.RequestVersion, Decision: in.Decision}
	raw := job.JSON(payload)
	req := ControlOperationRequest{
		OperationID: in.OperationID,
		OperationType: "approval",
		ResourceType: "approval",
		ResourceID: approvalID,
		TaskID: in.TaskID,
		ExpectedAttemptID: in.ExpectedAttemptID,
		ExpectedGeneration: in.ExpectedGeneration,
		ExpectedResourceVersion: in.ExpectedResourceVersion,
		Payload: raw,
	}
	receipt, err := s.acceptAndDispatchControlOperation(r.Context(), r.PathValue("id"), req)
	if err != nil {
		httpError(w, err)
		return
	}
	code := 202
	if receipt.Existing || receipt.State == "COMPLETED" || receipt.State == "REJECTED" {
		code = 200
	}
	jsonResponse(w, code, receipt)
}

