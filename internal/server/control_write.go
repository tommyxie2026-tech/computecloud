package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
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
		return status.Error(codes.Aborted, control.ErrorAttemptFenced)
	}
	var jobVersion int64
	var state string
	if err := q.QueryRowContext(ctx, "SELECT version,state FROM jobs WHERE id=?", jobID).
		Scan(&jobVersion, &state); err != nil {
		return err
	}
	if jobVersion != in.ExpectedResourceVersion {
		return status.Error(codes.Aborted, control.ErrorResourceVersionConflict)
	}
	if terminal(state) || state == "STOPPING" || state == "RECONCILING" {
		return status.Error(codes.FailedPrecondition, control.ErrorExecutionUnverifiable)
	}
	var released int
	if err := q.QueryRowContext(ctx,
		"SELECT released FROM attempts WHERE id=? AND task=? AND generation=?",
		in.ExpectedAttemptID, in.TaskID, in.ExpectedGeneration,
	).Scan(&released); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.Aborted, control.ErrorAttemptFenced)
		}
		return err
	}
	if released != 0 {
		return status.Error(codes.Aborted, control.ErrorAttemptFenced)
	}
	return nil
}


func hasControlCapability(values []control.Capability, want control.Capability) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Server) validateControlCapability(ctx context.Context, q store.Query, in ControlOperationRequest) error {
	if in.OperationType == "cancel" {
		// Cancel is backed by the existing server-owned stop command path and does
		// not require an interactive SessionControlProvider.
		return nil
	}
	var raw []byte
	var workerID string
	if err := q.QueryRowContext(ctx, "SELECT spec,worker FROM tasks WHERE id=?", in.TaskID).Scan(&raw, &workerID); err != nil {
		return err
	}
	spec := new(pb.TaskSpec)
	if err := decode(raw, spec); err != nil {
		return status.Error(codes.Unavailable, "invalid persisted task spec")
	}
	_, capabilities := s.runtimeProjection(workerID, spec.RuntimeProfile)
	required := control.Capability("")
	switch in.OperationType {
	case "input":
		required = control.CapabilityInteractiveInput
	case "interrupt":
		required = control.CapabilityInterrupt
	case "approval":
		required = control.CapabilityApproval
	case "resume":
		required = control.CapabilitySessionResume
	}
	if required == "" || !hasControlCapability(capabilities, required) {
		return status.Error(codes.FailedPrecondition, control.ErrorCapabilityUnsupported)
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
				return status.Error(codes.AlreadyExists, control.ErrorOperationConflict)
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
		if err := s.validateControlCapability(ctx, q, in); err != nil {
			return err
		}
		raw := job.JSON(receipt)
		now := store.Now()
		_, err := q.ExecContext(ctx, `INSERT INTO control_operations(
			principal_id,operation_id,operation_type,resource_type,resource_id,
			job_id,task_id,expected_attempt_id,expected_generation,expected_resource_version,
			request_hash,state,receipt_json,created,updated,request_json
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.Identity.Owner, in.OperationID, in.OperationType, in.ResourceType, in.ResourceID,
			jobID, in.TaskID, in.ExpectedAttemptID, in.ExpectedGeneration, in.ExpectedResourceVersion,
			hash, "ACCEPTED", raw, now, now, job.JSON(in))
		return err
	})
	if err != nil {
		return nil, dbErr(err)
	}
	return receipt, nil
}


func controlCommandID(principalID, operationID string) string {
	return "control-" + store.Hash([]byte(principalID+"\x00"+operationID))[:32]
}

func (s *Server) dispatchControlOperation(ctx context.Context, jobID string, in ControlOperationRequest, receipt *ControlOperationReceipt) error {
	p, err := rpcutil.Require(ctx, "jobs:control", false)
	if err != nil {
		return err
	}
	return s.dispatchControlOperationPrincipal(ctx, p.Identity.Owner, jobID, in, receipt)
}

func (s *Server) dispatchControlOperationPrincipal(ctx context.Context, principalID, jobID string, in ControlOperationRequest, receipt *ControlOperationReceipt) error {
	return s.db.Tx(ctx, func(q store.Query) error {
		if err := validateControlFence(ctx, q, jobID, in); err != nil {
			return err
		}
		// Capability and authorization were checked at the durable acceptance
		// boundary. Recovery must not depend on the Worker still being online.
		if in.OperationType == "cancel" {
			receipt.State = "DISPATCHED"
			raw := job.JSON(receipt)
			_, err := q.ExecContext(ctx,
				"UPDATE control_operations SET state='DISPATCHED',receipt_json=?,updated=? WHERE principal_id=? AND operation_id=?",
				raw, store.Now(), principalID, in.OperationID)
			return err
		}

		var workerID, leaseToken, nativeSession string
		var generation int64
		if err := q.QueryRowContext(ctx, `SELECT t.worker,a.token,a.generation,t.native_session
			FROM tasks t JOIN attempts a ON a.id=t.attempt
			WHERE t.id=? AND t.job_id=? AND a.id=? AND a.released=0`,
			in.TaskID, jobID, in.ExpectedAttemptID,
		).Scan(&workerID, &leaseToken, &generation, &nativeSession); err != nil {
			return err
		}
		if generation != in.ExpectedGeneration {
			return status.Error(codes.Aborted, control.ErrorAttemptFenced)
		}
		cc := &pb.ControlCommand{
			OperationId: in.OperationID,
			OperationType: in.OperationType,
			JobId: jobID,
			TaskId: in.TaskID,
			AttemptId: in.ExpectedAttemptID,
			Generation: in.ExpectedGeneration,
			SessionRef: nativeSession,
			PayloadJson: append([]byte(nil), in.Payload...),
			LeaseToken: leaseToken,
		}
		if in.OperationType == "input" {
			var payload struct{ Mode string `json:"mode"` }
			if err := json.Unmarshal(in.Payload, &payload); err != nil {
				return status.Error(codes.InvalidArgument, "invalid control input payload")
			}
			cc.Mode = payload.Mode
		}
		cmd := &pb.Command{
			CommandId: controlCommandID(principalID, in.OperationID),
			Kind: "control",
			Control: cc,
		}
		body := encode(cmd)
		var existing []byte
		err := q.QueryRowContext(ctx, "SELECT body FROM commands WHERE id=?", cmd.CommandId).Scan(&existing)
		switch {
		case err == nil:
			if store.Hash(existing) != store.Hash(body) {
				return status.Error(codes.AlreadyExists, control.ErrorOperationConflict)
			}
		case errors.Is(err, sql.ErrNoRows):
			if _, err = q.ExecContext(ctx,
				"INSERT INTO commands(id,task,attempt,worker,kind,body) VALUES(?,?,?,?,?,?)",
				cmd.CommandId, in.TaskID, in.ExpectedAttemptID, workerID, "control", body); err != nil {
				return err
			}
		default:
			return err
		}

		receipt.State = "DISPATCHED"
		raw := job.JSON(receipt)
		_, err = q.ExecContext(ctx,
			"UPDATE control_operations SET state='DISPATCHED',receipt_json=?,updated=? WHERE principal_id=? AND operation_id=?",
			raw, store.Now(), principalID, in.OperationID)
		return err
	})
}

func (s *Server) recoverAcceptedControlOperations(ctx context.Context) error {
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT principal_id,job_id,request_json,receipt_json
		FROM control_operations WHERE state='ACCEPTED' ORDER BY created LIMIT 64`)
	if err != nil {
		return err
	}
	type pending struct {
		principal, jobID string
		request, receipt []byte
	}
	var list []pending
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.principal, &item.jobID, &item.request, &item.receipt); err != nil {
			break
		}
		list = append(list, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, item := range list {
		var in ControlOperationRequest
		var receipt ControlOperationReceipt
		if err = json.Unmarshal(item.request, &in); err != nil {
			return err
		}
		if err = json.Unmarshal(item.receipt, &receipt); err != nil {
			return err
		}
		if err = s.dispatchControlOperationPrincipal(ctx, item.principal, item.jobID, in, &receipt); err != nil {
			switch status.Code(err) {
			case codes.Aborted, codes.FailedPrecondition, codes.NotFound:
				receipt.State = "REJECTED"
				_ = s.db.Tx(ctx, func(q store.Query) error {
					_, updateErr := q.ExecContext(ctx, `UPDATE control_operations
						SET state='REJECTED',receipt_json=?,updated=?
						WHERE principal_id=? AND operation_id=? AND state='ACCEPTED'`,
						job.JSON(receipt), store.Now(), item.principal, in.OperationID)
					return updateErr
				})
				continue
			default:
				return err
			}
		}
	}
	return nil
}

func (s *Server) acceptAndDispatchControlOperation(ctx context.Context, jobID string, in ControlOperationRequest) (*ControlOperationReceipt, error) {
	receipt, err := s.acceptControlOperation(ctx, jobID, in)
	if err != nil {
		return nil, err
	}
	if err = s.dispatchControlOperation(ctx, jobID, in, receipt); err != nil {
		return nil, dbErr(err)
	}
	return receipt, nil
}


func resolveControlOperationEvent(ctx context.Context, q store.Query, event *pb.Event) error {
	if event == nil || (event.Type != "control.accepted" && event.Type != "control.rejected") {
		return nil
	}
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(event.PayloadJson, &payload); err != nil || payload.OperationID == "" {
		return status.Error(codes.InvalidArgument, "invalid control result event")
	}
	var principalID string
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT principal_id,receipt_json
		FROM control_operations
		WHERE operation_id=? AND task_id=? AND expected_attempt_id=? AND expected_generation=?`,
		payload.OperationID, event.TaskId, event.AttemptId, event.Generation,
	).Scan(&principalID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var receipt ControlOperationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return err
	}
	next := "COMPLETED"
	if event.Type == "control.rejected" {
		next = "REJECTED"
	}
	receipt.State = next
	receipt.Existing = false
	_, err = q.ExecContext(ctx, `UPDATE control_operations
		SET state=?,receipt_json=?,updated=?
		WHERE principal_id=? AND operation_id=? AND state IN ('ACCEPTED','DISPATCHED')`,
		next, job.JSON(receipt), store.Now(), principalID, payload.OperationID)
	return err
}
