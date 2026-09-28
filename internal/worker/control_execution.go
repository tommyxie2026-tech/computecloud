package worker

import (
	"context"
	"database/sql"
	"errors"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func hasControlCapability(desc adapter.ControlDescriptor, want control.Capability) bool {
	for _, capability := range desc.Capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func controlCapability(cmd *pb.ControlCommand) (control.Capability, error) {
	if cmd == nil {
		return "", errors.New("control command required")
	}
	switch cmd.Action {
	case "input":
		switch cmd.Mode {
		case "", "interactive":
			return control.CapabilityInteractiveInput, nil
		case "queue_next":
			return control.CapabilityQueueNextInput, nil
		case "steer_current":
			return control.CapabilitySteerCurrent, nil
		default:
			return "", errors.New("unsupported input mode")
		}
	case "interrupt":
		return control.CapabilityInterrupt, nil
	default:
		return "", errors.New("unsupported control action")
	}
}

func (w *Worker) controlAck(commandID string, cmd *pb.ControlCommand, state, code, message string) *pb.CommandAck {
	operationID := ""
	if cmd != nil {
		operationID = cmd.OperationId
	}
	return &pb.CommandAck{
		CommandId: commandID,
		State: state,
		OperationId: operationID,
		ErrorCode: code,
		ErrorMessage: message,
	}
}

// executeControl executes a structured Agent Control command at-most-once with
// respect to automatic retries. If the Worker crashes while the command is
// EXECUTING, recovery marks it UNKNOWN and future delivery returns
// EXECUTION_UNVERIFIABLE instead of re-running a potentially completed side
// effect.
func (w *Worker) executeControl(ctx context.Context, c *pb.Command) (*pb.CommandAck, error) {
	if c == nil || c.CommandId == "" || c.Kind != "control" || c.Control == nil {
		return nil, errors.New("invalid control command")
	}
	cmd := c.Control
	if cmd.OperationId == "" || cmd.TaskId == "" || cmd.AttemptId == "" || cmd.Generation < 1 {
		return nil, errors.New("invalid control command identity")
	}
	hash := store.Hash(enc(c))
	var replay *pb.CommandAck
	err := w.db.Tx(ctx, func(q store.Query) error {
		var oldHash, state, code, message string
		e := q.QueryRowContext(ctx, "SELECT hash,state,error_code,error_message FROM commands WHERE id=?", c.CommandId).
			Scan(&oldHash, &state, &code, &message)
		if e == nil {
			if oldHash != hash {
				return errors.New("command hash conflict")
			}
			switch state {
			case "COMPLETED":
				replay = w.controlAck(c.CommandId, cmd, "COMPLETED", code, message)
			case "REJECTED":
				replay = w.controlAck(c.CommandId, cmd, "REJECTED", code, message)
			case "UNKNOWN", "EXECUTING", "PENDING":
				replay = w.controlAck(c.CommandId, cmd, "UNKNOWN", control.ErrorExecutionUnverifiable.String(), "control execution outcome is unknown")
			default:
				return errors.New("invalid persisted control state")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var assignmentRaw, runtimeRefRaw []byte
		var runtimeProvider, runtimeState string
		e = q.QueryRowContext(ctx, "SELECT assignment,runtime_provider,runtime_ref,runtime_state FROM runs WHERE id=? AND completion IS NULL", cmd.AttemptId).
			Scan(&assignmentRaw, &runtimeProvider, &runtimeRefRaw, &runtimeState)
		if e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return errors.New("attempt unavailable for control")
			}
			return e
		}
		a := new(pb.Assignment)
		if e = dec(assignmentRaw, a); e != nil {
			return e
		}
		if a.TaskId != cmd.TaskId || a.AttemptId != cmd.AttemptId || a.Generation != cmd.Generation {
			return errors.New("control attempt identity conflict")
		}
		if runtimeProvider == "" || len(runtimeRefRaw) == 0 || runtimeState != string(adapter.RuntimeRunning) {
			_, e = q.ExecContext(ctx, "INSERT INTO commands(id,hash,state,attempt,operation_id,updated,error_code,error_message) VALUES(?,?,?,?,?,?,?,?)",
				c.CommandId, hash, "REJECTED", cmd.AttemptId, cmd.OperationId, store.Now(),
				control.ErrorExecutionUnverifiable.String(), "runtime is not in a controllable running state")
			if e == nil {
				replay = w.controlAck(c.CommandId, cmd, "REJECTED", control.ErrorExecutionUnverifiable.String(), "runtime is not in a controllable running state")
			}
			return e
		}
		_, e = q.ExecContext(ctx, "INSERT INTO commands(id,hash,state,attempt,operation_id,updated) VALUES(?,?,?,?,?,?)",
			c.CommandId, hash, "EXECUTING", cmd.AttemptId, cmd.OperationId, store.Now())
		return e
	})
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return replay, nil
	}

	var runtimeProvider string
	var runtimeRefRaw []byte
	if err = w.db.SQL.QueryRowContext(ctx,
		"SELECT runtime_provider,runtime_ref FROM runs WHERE id=?",
		cmd.AttemptId).Scan(&runtimeProvider, &runtimeRefRaw); err != nil {
		return nil, err
	}
	ref, err := decodeRuntimeRef(runtimeProvider, runtimeRefRaw)
	if err != nil {
		return w.finishControl(ctx, c, "UNKNOWN", control.ErrorExecutionUnverifiable.String(), err.Error())
	}
	provider, ok := adapter.Lookup(runtimeProvider)
	if !ok {
		return w.finishControl(ctx, c, "REJECTED", control.ErrorCapabilityUnsupported.String(), "runtime provider unavailable")
	}
	sessionProvider, ok := provider.(adapter.SessionControlProvider)
	if !ok {
		return w.finishControl(ctx, c, "REJECTED", control.ErrorCapabilityUnsupported.String(), "runtime does not implement SessionControlProvider")
	}
	desc, claimed, err := adapter.ControlDescriptorFor(provider)
	if err != nil || !claimed {
		if err == nil {
			err = errors.New("runtime has no control descriptor")
		}
		return w.finishControl(ctx, c, "REJECTED", control.ErrorCapabilityUnsupported.String(), err.Error())
	}
	required, err := controlCapability(cmd)
	if err != nil {
		return w.finishControl(ctx, c, "REJECTED", control.ErrorCapabilityUnsupported.String(), err.Error())
	}
	if !hasControlCapability(desc, required) {
		return w.finishControl(ctx, c, "REJECTED", control.ErrorCapabilityUnsupported.String(), "runtime control capability not certified")
	}

	switch cmd.Action {
	case "input":
		text := ""
		if cmd.Input != nil {
			text = cmd.Input.Text
		}
		err = sessionProvider.Input(ctx, adapter.ControlInputRequest{
			Ref: ref, SessionRef: cmd.SessionRef, AttemptID: cmd.AttemptId,
			Generation: cmd.Generation, Mode: cmd.Mode, Content: text,
		})
	case "interrupt":
		err = sessionProvider.Interrupt(ctx, adapter.ControlInterruptRequest{
			Ref: ref, SessionRef: cmd.SessionRef, AttemptID: cmd.AttemptId,
			Generation: cmd.Generation,
		})
	default:
		err = adapter.ErrControlCapabilityUnsupported
	}
	if err != nil {
		code := "RUNTIME_CONTROL_FAILED"
		if errors.Is(err, adapter.ErrControlCapabilityUnsupported) {
			code = control.ErrorCapabilityUnsupported.String()
		}
		return w.finishControl(ctx, c, "REJECTED", code, err.Error())
	}
	return w.finishControl(ctx, c, "COMPLETED", "", "")
}

func (w *Worker) finishControl(ctx context.Context, c *pb.Command, state, code, message string) (*pb.CommandAck, error) {
	cmd := c.Control
	if state == "UNKNOWN" {
		code = control.ErrorExecutionUnverifiable.String()
	}
	_, err := w.db.SQL.ExecContext(ctx, "UPDATE commands SET state=?,error_code=?,error_message=?,updated=? WHERE id=? AND operation_id=?",
		state, code, message, store.Now(), c.CommandId, cmd.OperationId)
	if err != nil {
		return nil, err
	}
	return w.controlAck(c.CommandId, cmd, state, code, message), nil
}
