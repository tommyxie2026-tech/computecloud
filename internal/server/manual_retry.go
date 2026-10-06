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

type ManualRetryRequest struct {
	OperationID        string `json:"operation_id"`
	TaskID             string `json:"task_id"`
	ExpectedAttemptID  string `json:"expected_attempt_id"`
	ExpectedGeneration int64  `json:"expected_generation"`
}

type ManualRetryReceipt struct {
	OperationID    string `json:"operation_id"`
	JobID          string `json:"job_id"`
	TaskID         string `json:"task_id"`
	AttemptID      string `json:"attempt_id"`
	Generation     int64  `json:"generation"`
	NextGeneration int64  `json:"next_generation"`
	State          string `json:"state"`
	Existing       bool   `json:"existing,omitempty"`
}

func validateManualRetryRequest(in ManualRetryRequest) error {
	if !job.ValidKey(in.OperationID) || in.TaskID == "" || in.ExpectedAttemptID == "" || in.ExpectedGeneration < 1 {
		return status.Error(codes.InvalidArgument, "invalid manual retry request")
	}
	return nil
}

func manualRetryNotAllowed(reason string) error {
	if reason == "" {
		reason = "MANUAL_RETRY_NOT_ALLOWED"
	}
	return status.Error(codes.FailedPrecondition, reason)
}

// ManualRetry reopens exactly one failed single-mode Job using the frozen Job
// spec. It deliberately does not weaken the generic terminal Job/Stage guards.
// Map/Reduce retry requires graph-aware sibling/barrier recovery and is not part
// of this bounded C2 contract.
func (s *Server) ManualRetry(ctx context.Context, jobID string, in ManualRetryRequest) (*ManualRetryReceipt, error) {
	if err := validateManualRetryRequest(in); err != nil {
		return nil, err
	}
	if _, err := s.jobAuthorized(ctx, jobID, "jobs:retry"); err != nil {
		return nil, err
	}

	digest := job.Hash(job.JSON(in))
	opKey := "manual-retry:" + in.OperationID
	var receipt ManualRetryReceipt
	err := s.db.Tx(ctx, func(q store.Query) error {
		var oldHash string
		var oldBody []byte
		err := q.QueryRowContext(ctx,
			"SELECT operation_hash,body FROM job_events WHERE job_id=? AND operation_key=?",
			jobID, opKey,
		).Scan(&oldHash, &oldBody)
		if err == nil {
			if oldHash != digest {
				return status.Error(codes.AlreadyExists, control.ErrorOperationConflict.String())
			}
			var body map[string]any
			if err = json.Unmarshal(oldBody, &body); err != nil {
				return err
			}
			receipt = ManualRetryReceipt{
				OperationID:    in.OperationID,
				JobID:          jobID,
				TaskID:         in.TaskID,
				AttemptID:      in.ExpectedAttemptID,
				Generation:     in.ExpectedGeneration,
				NextGeneration: in.ExpectedGeneration + 1,
				State:          "ACCEPTED",
				Existing:       true,
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		j, err := readJob(ctx, q, jobID)
		if err != nil {
			return err
		}
		if j.Mode != "single" {
			return manualRetryNotAllowed("MANUAL_RETRY_SINGLE_ONLY")
		}
		if j.State != "FAILED" || j.StopReason != "CHILD_FAILED" {
			return manualRetryNotAllowed("MANUAL_RETRY_JOB_STATE")
		}
		if j.Deadline <= store.Now() {
			return manualRetryNotAllowed("MANUAL_RETRY_DEADLINE_EXHAUSTED")
		}

		var taskState, attemptID, stage, partitionKey, errorCode string
		var generation int64
		err = q.QueryRowContext(ctx, `SELECT state,attempt,current_generation,stage,partition_key,error_code
			FROM tasks WHERE id=? AND job_id=?`, in.TaskID, jobID).
			Scan(&taskState, &attemptID, &generation, &stage, &partitionKey, &errorCode)
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "NOT_FOUND")
		}
		if err != nil {
			return err
		}
		if attemptID != in.ExpectedAttemptID || generation != in.ExpectedGeneration {
			return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
		}
		if taskState != "FAILED" {
			return manualRetryNotAllowed("MANUAL_RETRY_TASK_STATE")
		}
		var released bool
		var attemptGeneration int64
		if err = q.QueryRowContext(ctx, "SELECT released,generation FROM attempts WHERE id=? AND task=?",
			attemptID, in.TaskID).Scan(&released, &attemptGeneration); err != nil {
			return err
		}
		if attemptGeneration != generation {
			return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
		}
		if !released {
			return manualRetryNotAllowed(control.ErrorExecutionUnverifiable.String())
		}

		execution, ok := j.frozen.Spec.ExecutionFor(stage, partitionKey)
		if !ok || !execution.ReplaySafe {
			return manualRetryNotAllowed("MANUAL_RETRY_REPLAY_UNSAFE")
		}
		if generation >= int64(j.frozen.Spec.Limits.MaxAttemptsPerTask) {
			return manualRetryNotAllowed("MANUAL_RETRY_ATTEMPTS_EXHAUSTED")
		}

		var stageID, stageState string
		if err = q.QueryRowContext(ctx, "SELECT stage_id FROM tasks WHERE id=?", in.TaskID).Scan(&stageID); err != nil {
			return err
		}
		if err = q.QueryRowContext(ctx, "SELECT state FROM stages WHERE id=? AND job_id=?", stageID, jobID).Scan(&stageState); err != nil {
			return err
		}
		if stageState != "FAILED" {
			return manualRetryNotAllowed("MANUAL_RETRY_STAGE_STATE")
		}

		now := store.Now()
		res, err := q.ExecContext(ctx, `UPDATE tasks
			SET state='QUEUED',attempt='',worker='',error_code='',error_message='',result='',native_session='',
			    blocker='',retry_after=0,updated=?
			WHERE id=? AND job_id=? AND state='FAILED' AND attempt=? AND current_generation=?`,
			now, in.TaskID, jobID, attemptID, generation)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return status.Error(codes.Aborted, control.ErrorAttemptFenced.String())
		}

		res, err = q.ExecContext(ctx, "UPDATE stages SET state='READY',updated=? WHERE id=? AND job_id=? AND state='FAILED'",
			now, stageID, jobID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return manualRetryNotAllowed("MANUAL_RETRY_STAGE_STATE")
		}

		res, err = q.ExecContext(ctx, `UPDATE jobs
			SET state='QUEUED',stop_reason='',error_code='',result_json=NULL,version=version+1,updated=?
			WHERE id=? AND state='FAILED' AND version=?`, now, jobID, j.Version)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return status.Error(codes.Aborted, "RESOURCE_VERSION_CONFLICT")
		}

		// Explicit bounded manual retry reopens execution of the same frozen plan.
		// It does not reset Goal counters or create a Re-plan revision.
		if _, err = q.ExecContext(ctx, `UPDATE goals SET state='RUNNING',version=version+1,updated=? WHERE id=(SELECT goal_id FROM goal_job_bindings WHERE job_id=?) AND state='FAILED'`, store.Now(), jobID); err != nil {
			return err
		}
		if err = appendEvent(ctx, q, &pb.Event{
			TaskId:     in.TaskID,
			AttemptId:  attemptID,
			Generation: generation,
			Type:       "task.manual_retry_requested",
			PayloadJson: job.JSON(map[string]any{
				"operation_id":    in.OperationID,
				"error_code":      errorCode,
				"next_generation": generation + 1,
			}),
		}, nil); err != nil {
			return err
		}

		receipt = ManualRetryReceipt{
			OperationID:    in.OperationID,
			JobID:          jobID,
			TaskID:         in.TaskID,
			AttemptID:      attemptID,
			Generation:     generation,
			NextGeneration: generation + 1,
			State:          "ACCEPTED",
		}
		return appendJobEvent(ctx, q, jobID, "job.task_manual_retry_requested", receipt, opKey, digest)
	})
	if err != nil {
		return nil, dbErr(err)
	}
	s.wake()
	return &receipt, nil
}

func (s *Server) httpManualRetry(w http.ResponseWriter, r *http.Request) {
	if err := s.requireControlWriteLease(r, r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	b, err := readJSONBody(w, r, 16<<10)
	if err != nil {
		httpError(w, err)
		return
	}
	var in ManualRetryRequest
	if err = json.Unmarshal(b, &in); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid manual retry JSON"))
		return
	}
	receipt, err := s.ManualRetry(r.Context(), r.PathValue("id"), in)
	if err != nil {
		httpError(w, err)
		return
	}
	code := http.StatusAccepted
	if receipt.Existing {
		code = http.StatusOK
	}
	jsonResponse(w, code, receipt)
}
