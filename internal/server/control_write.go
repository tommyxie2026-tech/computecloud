package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SessionControlRequest struct {
	OperationID        string `json:"operation_id"`
	ExpectedAttemptID  string `json:"expected_attempt_id"`
	ExpectedGeneration int64  `json:"expected_generation"`
	Mode               string `json:"mode,omitempty"`
	Content            string `json:"content,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

type controlSessionRow struct {
	taskID            string
	attemptID         string
	currentAttemptID  string
	state             string
	workerID          string
	spec              []byte
	generation        int64
	currentGeneration int64
	released          bool
}

func validateSessionControlRequest(sessionID string, in SessionControlRequest) error {
	if !job.ValidKey(in.OperationID) || in.ExpectedAttemptID == "" || in.ExpectedAttemptID != sessionID || in.ExpectedGeneration < 1 {
		return status.Error(codes.InvalidArgument, "invalid control operation")
	}
	if len(in.Reason) > 1000 || len(in.Mode) > 64 || len(in.Content) > 64<<10 {
		return status.Error(codes.InvalidArgument, "control operation exceeds limits")
	}
	return nil
}

func readControlSession(ctx context.Context, q store.Query, jobID, sessionID string) (controlSessionRow, error) {
	var row controlSessionRow
	err := q.QueryRowContext(ctx, `SELECT a.task,a.id,a.generation,a.released,
		t.attempt,t.current_generation,t.state,t.worker,t.spec
		FROM attempts a
		JOIN tasks t ON t.id=a.task
		WHERE a.id=? AND t.job_id=?`, sessionID, jobID).Scan(
		&row.taskID, &row.attemptID, &row.generation, &row.released,
		&row.currentAttemptID, &row.currentGeneration, &row.state, &row.workerID, &row.spec,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return row, status.Error(codes.NotFound, "NOT_FOUND")
	}
	return row, err
}

func persistedControlCapabilities(ctx context.Context, q store.Query, workerID, profile string) ([]control.Capability, error) {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT hello FROM workers WHERE id=?", workerID).Scan(&raw); err != nil {
		return nil, err
	}
	hello := new(pb.WorkerHello)
	if err := decode(raw, hello); err != nil {
		return nil, err
	}
	for _, runtime := range hello.Runtimes {
		if runtime.Profile == profile {
			return controlCapabilities(runtime.Capabilities), nil
		}
	}
	return nil, nil
}

func hasControlCapability(values []control.Capability, want control.Capability) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sessionControlRequestHash(operationType, jobID, sessionID string, in SessionControlRequest) string {
	return job.Hash(job.JSON(struct {
		OperationType string                `json:"operation_type"`
		JobID         string                `json:"job_id"`
		SessionID     string                `json:"session_id"`
		Request       SessionControlRequest `json:"request"`
	}{
		OperationType: operationType,
		JobID:         jobID,
		SessionID:     sessionID,
		Request:       in,
	}))
}

func readExistingControlOperation(ctx context.Context, q store.Query, principal, operationID, requestHash string) (*control.OperationReceipt, bool, error) {
	var previousHash string
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT request_hash,receipt
		FROM control_operations
		WHERE principal_id=? AND operation_id=?`, principal, operationID).Scan(&previousHash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if previousHash != requestHash {
		return nil, true, status.Error(codes.AlreadyExists, "OPERATION_CONFLICT")
	}
	var receipt control.OperationReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return nil, true, err
	}
	if err = receipt.Validate(); err != nil {
		return nil, true, err
	}
	receipt.Existing = true
	return &receipt, true, nil
}

func insertControlOperation(ctx context.Context, q store.Query, principal, requestHash string, receipt control.OperationReceipt) error {
	raw := job.JSON(receipt)
	now := store.Now()
	_, err := q.ExecContext(ctx, `INSERT INTO control_operations(
		principal_id,operation_id,operation_type,resource_type,resource_id,job_id,
		session_id,task_id,attempt_id,generation,resource_version,
		request_hash,state,receipt,created,updated
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		principal, receipt.OperationID, receipt.OperationType, receipt.ResourceType, receipt.ResourceID, receipt.JobID,
		receipt.SessionID, receipt.TaskID, receipt.AttemptID, receipt.Generation, receipt.ResourceVersion,
		requestHash, string(receipt.State), raw, now, now,
	)
	return err
}

func (s *Server) rejectUnsupportedSessionControl(ctx context.Context, jobID, sessionID, scope string, capability control.Capability, in SessionControlRequest) error {
	if err := validateSessionControlRequest(sessionID, in); err != nil {
		return err
	}
	if _, err := s.jobAuthorized(ctx, jobID, scope); err != nil {
		return err
	}
	session, err := s.JobSession(ctx, jobID, sessionID)
	if err != nil {
		return err
	}
	if session.AttemptID != in.ExpectedAttemptID || session.Generation != in.ExpectedGeneration {
		return status.Error(codes.Aborted, "ATTEMPT_FENCED")
	}
	if !hasControlCapability(session.Capabilities, capability) {
		return status.Error(codes.FailedPrecondition, "CAPABILITY_UNSUPPORTED")
	}
	// A Runtime must not advertise a capability until the Worker command path is
	// implemented and covered by contract tests. Reaching this branch is therefore
	// a server/runtime compatibility failure, not permission to fake the action.
	return status.Error(codes.FailedPrecondition, "CAPABILITY_UNSUPPORTED")
}

func decodeSessionControlRequest(w http.ResponseWriter, r *http.Request) (SessionControlRequest, error) {
	var in SessionControlRequest
	body, err := readJSONBody(w, r, 96<<10)
	if err != nil {
		return in, err
	}
	if err = json.Unmarshal(body, &in); err != nil {
		return in, status.Error(codes.InvalidArgument, "invalid control operation JSON")
	}
	return in, nil
}

func (s *Server) httpSessionInput(w http.ResponseWriter, r *http.Request) {
	in, err := decodeSessionControlRequest(w, r)
	if err == nil {
		err = s.rejectUnsupportedSessionControl(r.Context(), r.PathValue("id"), r.PathValue("session"), "jobs:control", control.CapabilityInteractiveInput, in)
	}
	if err != nil {
		httpError(w, err)
		return
	}
	httpError(w, status.Error(codes.FailedPrecondition, "CAPABILITY_UNSUPPORTED"))
}

func (s *Server) httpSessionInterrupt(w http.ResponseWriter, r *http.Request) {
	in, err := decodeSessionControlRequest(w, r)
	if err == nil {
		err = s.rejectUnsupportedSessionControl(r.Context(), r.PathValue("id"), r.PathValue("session"), "jobs:control", control.CapabilityInterrupt, in)
	}
	if err != nil {
		httpError(w, err)
		return
	}
	httpError(w, status.Error(codes.FailedPrecondition, "CAPABILITY_UNSUPPORTED"))
}
