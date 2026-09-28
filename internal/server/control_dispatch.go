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

type controlInputPayload struct {
	Mode    string `json:"mode"`
	Content string `json:"content"`
}

func requiredControlCapability(in ControlOperationRequest) (control.Capability, controlInputPayload, error) {
	var payload controlInputPayload
	switch in.OperationType {
	case "input":
		if len(in.Payload) == 0 {
			return "", payload, status.Error(codes.InvalidArgument, "control input payload required")
		}
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			return "", payload, status.Error(codes.InvalidArgument, "invalid control input payload")
		}
		if payload.Content == "" {
			return "", payload, status.Error(codes.InvalidArgument, "control input content required")
		}
		switch payload.Mode {
		case "", "interactive":
			return control.CapabilityInteractiveInput, payload, nil
		case "queue_next":
			return control.CapabilityQueueNextInput, payload, nil
		case "steer_current":
			return control.CapabilitySteerCurrent, payload, nil
		default:
			return "", payload, status.Error(codes.InvalidArgument, "invalid control input mode")
		}
	case "interrupt":
		return control.CapabilityInterrupt, payload, nil
	default:
		return "", payload, status.Error(codes.FailedPrecondition, control.ErrorCapabilityUnsupported.String())
	}
}

func (s *Server) workerHasControlCapability(workerID, profile string, want control.Capability) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	peer := s.peers[workerID]
	if peer == nil {
		return false
	}
	for _, runtime := range peer.hello.Runtimes {
		if runtime.Profile != profile {
			continue
		}
		for _, capability := range controlCapabilities(runtime.Capabilities) {
			if capability == want {
				return true
			}
		}
	}
	return false
}

// acceptAndDispatchControlOperation is the ACP-3b write boundary. It validates
// the currently connected Worker capability before durable acceptance, then
// persists a structured Worker command in a separate transaction. The durable
// ACCEPTED receipt never implies execution; DISPATCHED only means the command
// is durably queued for the fenced Attempt generation.
func (s *Server) acceptAndDispatchControlOperation(ctx context.Context, jobID string, in ControlOperationRequest) (*ControlOperationReceipt, error) {
	principal, err := rpcutil.Require(ctx, "jobs:control", false)
	if err != nil {
		return nil, err
	}
	if err = validateControlOperationRequest(in); err != nil {
		return nil, err
	}
	j, err := s.jobAuthorized(ctx, jobID, "jobs:read")
	if err != nil {
		return nil, err
	}
	if j.owner != principal.Identity.Owner {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}

	hash := job.Hash(job.JSON(in))
	var oldHash string
	var oldReceipt []byte
	readErr := s.db.SQL.QueryRowContext(ctx, "SELECT request_hash,receipt_json FROM control_operations WHERE principal_id=? AND operation_id=?",
		principal.Identity.Owner, in.OperationID).Scan(&oldHash, &oldReceipt)
	if readErr == nil {
		if oldHash != hash {
			return nil, status.Error(codes.AlreadyExists, control.ErrorOperationConflict.String())
		}
		var receipt ControlOperationReceipt
		if err = json.Unmarshal(oldReceipt, &receipt); err != nil {
			return nil, dbErr(err)
		}
		receipt.Existing = true
		if receipt.State == "DISPATCHED" || receipt.State == "COMPLETED" || receipt.State == "REJECTED" {
			return &receipt, nil
		}
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return nil, dbErr(readErr)
	}

	if err = s.db.Tx(ctx, func(q store.Query) error {
		return validateControlFence(ctx, q, jobID, in)
	}); err != nil {
		return nil, dbErr(err)
	}
	required, payload, err := requiredControlCapability(in)
	if err != nil {
		return nil, err
	}
	var workerID string
	var specRaw []byte
	if err = s.db.SQL.QueryRowContext(ctx, "SELECT worker,spec FROM tasks WHERE id=? AND job_id=?", in.TaskID, jobID).
		Scan(&workerID, &specRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "NOT_FOUND")
		}
		return nil, dbErr(err)
	}
	spec := new(pb.TaskSpec)
	if err = decode(specRaw, spec); err != nil {
		return nil, status.Error(codes.Unavailable, "invalid persisted task spec")
	}
	if !s.workerHasControlCapability(workerID, spec.RuntimeProfile, required) {
		return nil, status.Error(codes.FailedPrecondition, control.ErrorCapabilityUnsupported.String())
	}
	receipt, err := s.acceptControlOperation(ctx, jobID, in)
	if err != nil {
		return nil, err
	}
	if err = s.dispatchControlOperation(ctx, principal.Identity.Owner, jobID, in, payload); err != nil {
		return nil, err
	}
	receipt.State = "DISPATCHED"
	return receipt, nil
}

func (s *Server) dispatchControlOperation(ctx context.Context, principalID, jobID string, in ControlOperationRequest, payload controlInputPayload) error {
	commandID := "control-" + store.Hash([]byte(principalID+"\x00"+in.OperationID))
	err := s.db.Tx(ctx, func(q store.Query) error {
		var state string
		if err := q.QueryRowContext(ctx, "SELECT state FROM control_operations WHERE principal_id=? AND operation_id=?",
			principalID, in.OperationID).Scan(&state); err != nil {
			return err
		}
		if state == "COMPLETED" || state == "REJECTED" || state == "DISPATCHED" {
			return nil
		}
		if state != "ACCEPTED" {
			return status.Error(codes.FailedPrecondition, control.ErrorExecutionUnverifiable.String())
		}
		if err := validateControlFence(ctx, q, jobID, in); err != nil {
			return err
		}
		var workerID, nativeSession string
		if err := q.QueryRowContext(ctx, "SELECT worker,native_session FROM tasks WHERE id=?", in.TaskID).
			Scan(&workerID, &nativeSession); err != nil {
			return err
		}
		cmd := &pb.Command{
			CommandId: commandID,
			Kind: "control",
			Control: &pb.ControlCommand{
				PrincipalId: principalID,
				OperationId: in.OperationID,
				TaskId: in.TaskID,
				AttemptId: in.ExpectedAttemptID,
				Generation: in.ExpectedGeneration,
				SessionRef: nativeSession,
				Action: in.OperationType,
				Mode: payload.Mode,
			},
		}
		if in.OperationType == "input" {
			cmd.Control.Input = &pb.Input{Text: payload.Content}
		}
		body := encode(cmd)
		var old []byte
		readErr := q.QueryRowContext(ctx, "SELECT body FROM commands WHERE id=?", commandID).Scan(&old)
		if readErr == nil {
			if store.Hash(old) != store.Hash(body) {
				return status.Error(codes.AlreadyExists, control.ErrorOperationConflict.String())
			}
		} else if errors.Is(readErr, sql.ErrNoRows) {
			if _, err := q.ExecContext(ctx, "INSERT INTO commands(id,task,attempt,worker,kind,body) VALUES(?,?,?,?,?,?)",
				commandID, in.TaskID, in.ExpectedAttemptID, workerID, "control", body); err != nil {
				return err
			}
		} else {
			return readErr
		}
		receipt := ControlOperationReceipt{
			OperationID: in.OperationID, State: "DISPATCHED", JobID: jobID,
			TaskID: in.TaskID, AttemptID: in.ExpectedAttemptID, Generation: in.ExpectedGeneration,
			ResourceVersion: in.ExpectedResourceVersion,
		}
		now := store.Now()
		if _, err := q.ExecContext(ctx, "UPDATE control_operations SET state='DISPATCHED',receipt_json=?,updated=? WHERE principal_id=? AND operation_id=? AND state='ACCEPTED'",
			job.JSON(receipt), now, principalID, in.OperationID); err != nil {
			return err
		}
		return appendJobEvent(ctx, q, jobID, "control.accepted", map[string]any{
			"protocol_version": control.ProtocolV1Alpha1,
			"operation_id": in.OperationID,
			"task_id": in.TaskID,
			"attempt_id": in.ExpectedAttemptID,
			"generation": in.ExpectedGeneration,
			"state": "DISPATCHED",
		}, "control-dispatch:"+principalID+":"+in.OperationID, store.Hash(body))
	})
	if err == nil {
		s.wake()
	}
	return dbErr(err)
}

func (s *Server) applyControlAck(ctx context.Context, workerID string, ack *pb.CommandAck) error {
	if ack == nil || ack.CommandId == "" || ack.OperationId == "" {
		return status.Error(codes.InvalidArgument, "invalid control command ACK")
	}
	if ack.State != "COMPLETED" && ack.State != "REJECTED" && ack.State != "UNKNOWN" {
		return status.Error(codes.InvalidArgument, "invalid control command ACK state")
	}
	return s.db.Tx(ctx, func(q store.Query) error {
		var body []byte
		if err := q.QueryRowContext(ctx, "SELECT body FROM commands WHERE id=? AND worker=? AND kind='control'",
			ack.CommandId, workerID).Scan(&body); err != nil {
			return err
		}
		cmd := new(pb.Command)
		if err := decode(body, cmd); err != nil {
			return err
		}
		if cmd.Control == nil || cmd.Control.OperationId != ack.OperationId || cmd.Control.PrincipalId == "" {
			return status.Error(codes.InvalidArgument, "control ACK identity mismatch")
		}
		ledgerState := "COMPLETED"
		eventType := "control.completed"
		code := ack.ErrorCode
		message := ack.ErrorMessage
		if ack.State != "COMPLETED" {
			ledgerState = "REJECTED"
			eventType = "control.rejected"
			if ack.State == "UNKNOWN" {
				code = control.ErrorExecutionUnverifiable.String()
				if message == "" {
					message = "worker could not prove whether control side effect completed"
				}
			}
		}
		var oldReceipt []byte
		if err := q.QueryRowContext(ctx, "SELECT receipt_json FROM control_operations WHERE principal_id=? AND operation_id=?",
			cmd.Control.PrincipalId, ack.OperationId).Scan(&oldReceipt); err != nil {
			return err
		}
		var receipt ControlOperationReceipt
		if err := json.Unmarshal(oldReceipt, &receipt); err != nil {
			return err
		}
		receipt.State = ledgerState
		receipt.ErrorCode = code
		receipt.ErrorMessage = message
		if _, err := q.ExecContext(ctx, "UPDATE control_operations SET state=?,receipt_json=?,updated=? WHERE principal_id=? AND operation_id=?",
			ledgerState, job.JSON(receipt), store.Now(), cmd.Control.PrincipalId, ack.OperationId); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "UPDATE commands SET acked=1 WHERE id=? AND worker=?",
			ack.CommandId, workerID); err != nil {
			return err
		}
		var jobID string
		if err := q.QueryRowContext(ctx, "SELECT job_id FROM control_operations WHERE principal_id=? AND operation_id=?",
			cmd.Control.PrincipalId, ack.OperationId).Scan(&jobID); err != nil {
			return err
		}
		return appendJobEvent(ctx, q, jobID, eventType, map[string]any{
			"protocol_version": control.ProtocolV1Alpha1,
			"operation_id": ack.OperationId,
			"task_id": cmd.Control.TaskId,
			"attempt_id": cmd.Control.AttemptId,
			"generation": cmd.Control.Generation,
			"state": ledgerState,
			"error_code": code,
			"error_message": message,
		}, "control-result:"+cmd.Control.PrincipalId+":"+ack.OperationId, store.Hash(job.JSON(map[string]any{
			"state": ledgerState, "error_code": code, "error_message": message,
		})))
	})
}
