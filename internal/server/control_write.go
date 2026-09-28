package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ControlOperationRequest struct {
	OperationID             string          `json:"operation_id"`
	OperationType           string          `json:"operation_type"`
	ResourceType            string          `json:"resource_type"`
	ResourceID              string          `json:"resource_id"`
	TaskID                  string          `json:"task_id"`
	ExpectedAttemptID       string          `json:"expected_attempt_id"`
	ExpectedGeneration      int64           `json:"expected_generation"`
	ExpectedResourceVersion int64           `json:"expected_resource_version"`
	Payload                 json.RawMessage `json:"payload,omitempty"`
}

type ControlOperationReceipt struct {
	OperationID   string `json:"operation_id"`
	State         string `json:"state"`
	Existing      bool   `json:"existing"`
	JobID         string `json:"job_id"`
	TaskID        string `json:"task_id"`
	AttemptID     string `json:"attempt_id"`
	Generation    int64  `json:"generation"`
	ResourceVersion int64 `json:"resource_version"`
}

func validateControlOperationRequest(in ControlOperationRequest) error {
	if !job.ValidKey(in.OperationID) ||
		in.OperationType == "" || in.ResourceType == "" || in.ResourceID == "" ||
		in.TaskID == "" || in.ExpectedAttemptID == "" ||
		in.ExpectedGeneration < 1 || in.ExpectedResourceVersion < 1 {
		return status.Error(codes.InvalidArgument, "invalid control operation")
	}
	switch in.OperationType {
	case "input", "interrupt", "cancel", "approval", "resume":
	default:
		return status.Error(codes.InvalidArgument, "invalid control operation type")
	}
	return nil
}

func validateControlFence(ctx context.Context, q store.Query, jobID string, in ControlOperationRequest) error {
	var currentAttempt string
	var currentGeneration int64
	var taskJob sql.NullString
	if err := q.QueryRowContext(ctx,
		"SELECT attempt,current_generation,job_id FROM tasks WHERE id=?",
		in.TaskID,
	).Scan(&currentAttempt, &currentGeneration, &taskJob); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "NOT_FOUND")
		}
		return err
	}
	if !taskJob.Valid || taskJob.String != jobID {
		return status.Error(codes.NotFound, "NOT_FOUND")
	}
	if currentAttempt != in.ExpectedAttemptID || currentGeneration != in.ExpectedGeneration {
		return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
	}
	var jobVersion int64
	var state string
	if err := q.QueryRowContext(ctx, "SELECT version,state FROM jobs WHERE id=?", jobID).
		Scan(&jobVersion, &state); err != nil {
		return err
	}
	if jobVersion != in.ExpectedResourceVersion {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict.String())
	}
	if terminal(state) || state == "STOPPING" || state == "RECONCILING" {
		return status.Error(codes.FailedPrecondition, control.ErrorExecutionUnverifiable.String())
	}
	var released int
	if err := q.QueryRowContext(ctx,
		"SELECT released FROM attempts WHERE id=? AND task=? AND generation=?",
		in.ExpectedAttemptID, in.TaskID, in.ExpectedGeneration,
	).Scan(&released); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
		}
		return err
	}
	if released != 0 {
		return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
	}
	return nil
}

// acceptControlOperation provides the durable exactly-once acceptance boundary
// for interactive control. Dispatch is deliberately a separate phase: a
// persisted ACCEPTED receipt never implies that a Runtime has executed it.
func (s *Server) acceptControlOperation(ctx context.Context, jobID string, in ControlOperationRequest) (*ControlOperationReceipt, error) {
	if err := validateControlOperationRequest(in); err != nil {
		return nil, err
	}
	p, err := rpcutil.Require(ctx, "jobs:control", false)
	if err != nil {
		return nil, err
	}
	j, err := s.jobAuthorized(ctx, jobID, "jobs:read")
	if err != nil {
		return nil, err
	}
	if j.owner != p.Identity.Owner {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}

	hash := job.Hash(job.JSON(in))
	receipt := &ControlOperationReceipt{
		OperationID: in.OperationID,
		State: "ACCEPTED",
		JobID: jobID,
		TaskID: in.TaskID,
		AttemptID: in.ExpectedAttemptID,
		Generation: in.ExpectedGeneration,
		ResourceVersion: in.ExpectedResourceVersion,
	}
	err = s.db.Tx(ctx, func(q store.Query) error {
		var oldHash, state string
		var oldReceipt []byte
		readErr := q.QueryRowContext(ctx, `SELECT request_hash,state,receipt_json
			FROM control_operations WHERE principal_id=? AND operation_id=?`,
			p.Identity.Owner, in.OperationID).Scan(&oldHash, &state, &oldReceipt)
		if readErr == nil {
			if oldHash != hash {
				return status.Error(codes.AlreadyExists, control.ErrorOperationConflict.String())
			}
			if err := json.Unmarshal(oldReceipt, receipt); err != nil {
				return err
			}
			receipt.Existing = true
			return nil
		}
		if !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		if err := validateControlFence(ctx, q, jobID, in); err != nil {
			return err
		}
		raw := job.JSON(receipt)
		now := store.Now()
		_, err := q.ExecContext(ctx, `INSERT INTO control_operations(
			principal_id,operation_id,operation_type,resource_type,resource_id,
			job_id,task_id,expected_attempt_id,expected_generation,expected_resource_version,
			request_hash,state,receipt_json,created,updated
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.Identity.Owner, in.OperationID, in.OperationType, in.ResourceType, in.ResourceID,
			jobID, in.TaskID, in.ExpectedAttemptID, in.ExpectedGeneration, in.ExpectedResourceVersion,
			hash, "ACCEPTED", raw, now, now)
		return err
	})
	if err != nil {
		return nil, dbErr(err)
	}
	return receipt, nil
}
