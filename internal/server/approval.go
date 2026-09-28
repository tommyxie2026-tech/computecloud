package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type approvalEventPayload struct {
	ApprovalID       string         `json:"approval_id"`
	SessionID        string         `json:"session_id"`
	Tool             string         `json:"tool"`
	Action           string         `json:"action"`
	RiskClass        string         `json:"risk_class"`
	ArgumentsSummary string         `json:"arguments_summary,omitempty"`
	PolicyContext    map[string]any `json:"policy_context,omitempty"`
	RequestVersion   int64          `json:"request_version"`
	ExpiresAtMS      int64          `json:"expires_at_ms,omitempty"`
}

func validApprovalRisk(value string) bool {
	switch value {
	case "LOW", "MEDIUM", "HIGH", "CRITICAL":
		return true
	default:
		return false
	}
}

func persistApprovalEvent(ctx context.Context, q store.Query, event *pb.Event) error {
	if event == nil || event.Type != "approval.requested" {
		return nil
	}
	var payload approvalEventPayload
	if err := json.Unmarshal(event.PayloadJson, &payload); err != nil {
		return status.Error(codes.InvalidArgument, "invalid approval request payload")
	}
	if payload.ApprovalID == "" || payload.SessionID == "" || payload.Tool == "" ||
		payload.Action == "" || !validApprovalRisk(payload.RiskClass) || payload.RequestVersion < 1 {
		return status.Error(codes.InvalidArgument, "invalid approval request payload")
	}
	var jobID sql.NullString
	var currentAttempt string
	var currentGeneration int64
	if err := q.QueryRowContext(ctx,
		"SELECT job_id,attempt,current_generation FROM tasks WHERE id=?",
		event.TaskId,
	).Scan(&jobID, &currentAttempt, &currentGeneration); err != nil {
		return err
	}
	if !jobID.Valid || currentAttempt != event.AttemptId || currentGeneration != event.Generation {
		return status.Error(codes.FailedPrecondition, control.ErrorAttemptFenced)
	}
	now := store.Now()
	policy := payload.PolicyContext
	if policy == nil {
		policy = map[string]any{}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO approval_requests(
		approval_id,request_version,job_id,task_id,attempt_id,generation,session_id,
		tool,action,risk_class,arguments_summary,policy_context_json,requested,expires,state,created,updated
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		payload.ApprovalID, payload.RequestVersion, jobID.String, event.TaskId,
		event.AttemptId, event.Generation, payload.SessionID, payload.Tool, payload.Action,
		payload.RiskClass, payload.ArgumentsSummary, job.JSON(policy), now, payload.ExpiresAtMS,
		string(control.ApprovalPending), now, now)
	if err != nil {
		return err
	}
	// A new request version supersedes any older still-pending version.
	_, err = q.ExecContext(ctx, `UPDATE approval_requests
		SET state='SUPERSEDED',updated=?
		WHERE approval_id=? AND request_version<? AND state='PENDING'`,
		now, payload.ApprovalID, payload.RequestVersion)
	return err
}

func validateApprovalDecision(ctx context.Context, q store.Query, in ControlOperationRequest) error {
	if in.OperationType != "approval" {
		return nil
	}
	var payload struct {
		ApprovalID     string `json:"approval_id"`
		RequestVersion int64  `json:"request_version"`
		Decision       string `json:"decision"`
	}
	if err := json.Unmarshal(in.Payload, &payload); err != nil ||
		payload.ApprovalID == "" || payload.RequestVersion < 1 ||
		(payload.Decision != "APPROVE" && payload.Decision != "REJECT") {
		return status.Error(codes.InvalidArgument, "invalid approval decision")
	}
	if in.ResourceID != payload.ApprovalID {
		return status.Error(codes.InvalidArgument, "approval resource mismatch")
	}
	var state string
	var attemptID string
	var generation, expires int64
	err := q.QueryRowContext(ctx, `SELECT state,attempt_id,generation,expires
		FROM approval_requests WHERE approval_id=? AND request_version=?`,
		payload.ApprovalID, payload.RequestVersion,
	).Scan(&state, &attemptID, &generation, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.NotFound, "NOT_FOUND")
	}
	if err != nil {
		return err
	}
	if state != string(control.ApprovalPending) ||
		attemptID != in.ExpectedAttemptID || generation != in.ExpectedGeneration {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict)
	}
	if expires > 0 && expires <= store.Now() {
		_, _ = q.ExecContext(ctx, `UPDATE approval_requests
			SET state='EXPIRED',updated=?
			WHERE approval_id=? AND request_version=? AND state='PENDING'`,
			store.Now(), payload.ApprovalID, payload.RequestVersion)
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict)
	}
	return nil
}

func resolveApprovalDecision(ctx context.Context, q store.Query, principalID, operationID string) error {
	var operationType, resourceID string
	var request []byte
	err := q.QueryRowContext(ctx, `SELECT operation_type,resource_id,request_json
		FROM control_operations WHERE principal_id=? AND operation_id=?`,
		principalID, operationID,
	).Scan(&operationType, &resourceID, &request)
	if err != nil || operationType != "approval" {
		return err
	}
	var in ControlOperationRequest
	if err = json.Unmarshal(request, &in); err != nil {
		return err
	}
	var payload struct {
		ApprovalID     string `json:"approval_id"`
		RequestVersion int64  `json:"request_version"`
		Decision       string `json:"decision"`
	}
	if err = json.Unmarshal(in.Payload, &payload); err != nil {
		return err
	}
	if payload.ApprovalID != resourceID {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict)
	}
	next := string(control.ApprovalRejected)
	if payload.Decision == "APPROVE" {
		next = string(control.ApprovalAccepted)
	}
	_, err = q.ExecContext(ctx, `UPDATE approval_requests SET state=?,updated=?
		WHERE approval_id=? AND request_version=? AND state='PENDING'`,
		next, store.Now(), payload.ApprovalID, payload.RequestVersion)
	return err
}
