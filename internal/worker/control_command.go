package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type controlResult struct {
	CommandID   string `json:"command_id"`
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	State       string `json:"state"`
	ErrorCode   string `json:"error_code,omitempty"`
	Message     string `json:"message,omitempty"`
}

func requiredControlCapability(c *pb.ControlCommand) (control.Capability, error) {
	if c == nil {
		return "", errors.New("control command required")
	}
	switch c.Action {
	case "input":
		switch c.Mode {
		case "queue_next":
			return control.CapabilityQueueNextInput, nil
		case "steer_current":
			return control.CapabilitySteerCurrent, nil
		case "":
			return control.CapabilityInteractiveInput, nil
		default:
			return "", errors.New("unsupported input mode")
		}
	case "interrupt":
		return control.CapabilityInterrupt, nil
	case "approval":
		return control.CapabilityApproval, nil
	case "resume":
		return control.CapabilitySessionResume, nil
	default:
		return "", errors.New("unsupported control action")
	}
}

func descriptorHasCapability(d adapter.ControlDescriptor, want control.Capability) bool {
	for _, got := range d.Capabilities {
		if got == want {
			return true
		}
	}
	// A provider that advertises generic interactive_input may accept an
	// unspecified input mode, but must explicitly advertise queue/steer modes.
	return false
}

func validateControlCommand(c *pb.ControlCommand) error {
	if c == nil || c.TaskId == "" || c.AttemptId == "" || c.Generation < 1 ||
		c.LeaseToken == "" || c.OperationId == "" {
		return errors.New("control identity required")
	}
	if _, err := requiredControlCapability(c); err != nil {
		return err
	}
	switch c.Action {
	case "input":
		if c.Input == nil || c.Input.Text == "" || len(c.Input.Text) > 1<<20 {
			return errors.New("bounded control input required")
		}
	case "approval":
		if c.ApprovalId == "" || c.RequestVersion < 1 ||
			(c.Decision != "accept" && c.Decision != "reject") {
			return errors.New("valid approval decision required")
		}
	case "resume":
		if c.SessionRef == "" {
			return errors.New("session_ref required for resume")
		}
	}
	return nil
}

// acceptControlCommand journals a structured control command before any Runtime
// side effect. A duplicate with the same hash is acknowledged but never executed
// again. This gives at-most-once dispatch across stream reconnects.
func (w *Worker) acceptControlCommand(ctx context.Context, cmd *pb.Command) (bool, error) {
	if cmd == nil || cmd.CommandId == "" || cmd.Kind != "control" {
		return false, errors.New("invalid control command envelope")
	}
	c := cmd.Control
	if err := validateControlCommand(c); err != nil {
		return false, err
	}
	fresh := false
	err := w.db.Tx(ctx, func(q store.Query) error {
		hash := store.Hash(enc(cmd))
		var oldHash string
		err := q.QueryRowContext(ctx, "SELECT hash FROM commands WHERE id=?", cmd.CommandId).Scan(&oldHash)
		if err == nil {
			if oldHash != hash {
				return errors.New("command hash conflict")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var rawAssignment, runtimeRef []byte
		var state, provider string
		var completion []byte
		err = q.QueryRowContext(ctx, `SELECT assignment,state,completion,runtime_provider,runtime_ref
			FROM runs WHERE id=?`, c.AttemptId).
			Scan(&rawAssignment, &state, &completion, &provider, &runtimeRef)
		if err != nil {
			return err
		}
		a := new(pb.Assignment)
		if err = dec(rawAssignment, a); err != nil {
			return err
		}
		if a.TaskId != c.TaskId || a.AttemptId != c.AttemptId ||
			a.Generation != c.Generation || a.LeaseToken != c.LeaseToken {
			return errors.New("control attempt identity conflict")
		}
		if state != "RUNNING" || len(completion) != 0 || provider == "" || len(runtimeRef) == 0 {
			return errors.New("runtime not controllable")
		}
		if _, err = q.ExecContext(ctx,
			`INSERT INTO commands(id,hash,kind,body,state) VALUES(?,?,?,?,?)`,
			cmd.CommandId, hash, "control", enc(cmd), "RECEIVED"); err != nil {
			return err
		}
		fresh = true
		return nil
	})
	return fresh, err
}

func (w *Worker) applyControlCommand(ctx context.Context, cmd *pb.Command) {
	c := cmd.GetControl()
	result := controlResult{
		CommandID: cmd.GetCommandId(), OperationID: c.GetOperationId(),
		Action: c.GetAction(), State: "COMPLETED",
	}
	if err := w.invokeControl(ctx, c); err != nil {
		result.State = "REJECTED"
		result.ErrorCode = "RUNTIME_CONTROL_FAILED"
		if errors.Is(err, adapter.ErrControlCapabilityUnsupported) {
			result.ErrorCode = "CAPABILITY_UNSUPPORTED"
		}
		result.Message = err.Error()
	}
	raw, _ := json.Marshal(result)
	state := result.State
	if _, err := w.db.SQL.ExecContext(ctx,
		"UPDATE commands SET state=?,result=? WHERE id=? AND state='RECEIVED'",
		state, raw, cmd.GetCommandId()); err != nil {
		return
	}
	// The event spool is durable and retried independently of the Worker stream.
	// If this emit fails, the local command result still proves that the command
	// must not be replayed.
	var assignmentRaw []byte
	if err := w.db.SQL.QueryRowContext(ctx,
		"SELECT assignment FROM runs WHERE id=?", c.GetAttemptId()).Scan(&assignmentRaw); err != nil {
		return
	}
	a := new(pb.Assignment)
	if err := dec(assignmentRaw, a); err != nil {
		return
	}
	eventType := "control.completed"
	if state != "COMPLETED" {
		eventType = "control.rejected"
	}
	_ = w.emit(ctx, a, eventType, raw)
}

func (w *Worker) invokeControl(ctx context.Context, c *pb.ControlCommand) error {
	if err := validateControlCommand(c); err != nil {
		return err
	}
	var providerName string
	var refRaw []byte
	var assignmentRaw []byte
	if err := w.db.SQL.QueryRowContext(ctx,
		`SELECT runtime_provider,runtime_ref,assignment FROM runs
		 WHERE id=? AND state='RUNNING' AND completion IS NULL`,
		c.AttemptId).Scan(&providerName, &refRaw, &assignmentRaw); err != nil {
		return err
	}
	a := new(pb.Assignment)
	if err := dec(assignmentRaw, a); err != nil {
		return err
	}
	if a.TaskId != c.TaskId || a.Generation != c.Generation || a.LeaseToken != c.LeaseToken {
		return errors.New("control attempt identity conflict")
	}
	ref, err := decodeRuntimeRef(providerName, refRaw)
	if err != nil {
		return err
	}
	provider, ok := adapter.Lookup(providerName)
	if !ok {
		return errors.New("runtime provider not registered")
	}
	sessionProvider, ok := provider.(adapter.SessionControlProvider)
	if !ok {
		return adapter.ErrControlCapabilityUnsupported
	}
	desc, claimed, err := adapter.ControlDescriptorFor(provider)
	if err != nil || !claimed {
		if err != nil {
			return err
		}
		return adapter.ErrControlCapabilityUnsupported
	}
	want, err := requiredControlCapability(c)
	if err != nil {
		return err
	}
	if !descriptorHasCapability(desc, want) {
		return adapter.ErrControlCapabilityUnsupported
	}
	switch c.Action {
	case "input":
		return sessionProvider.Input(ctx, adapter.ControlInputRequest{
			Ref: ref, SessionRef: c.SessionRef, AttemptID: c.AttemptId,
			Generation: c.Generation, Mode: c.Mode, Content: c.Input.Text,
		})
	case "interrupt":
		return sessionProvider.Interrupt(ctx, adapter.ControlInterruptRequest{
			Ref: ref, SessionRef: c.SessionRef, AttemptID: c.AttemptId,
			Generation: c.Generation,
		})
	case "approval":
		return sessionProvider.Approve(ctx, adapter.ControlApprovalRequest{
			Ref: ref, SessionRef: c.SessionRef, AttemptID: c.AttemptId,
			Generation: c.Generation, ApprovalID: c.ApprovalId,
			RequestVersion: c.RequestVersion, Decision: c.Decision,
		})
	case "resume":
		// Resume changes the durable RuntimeRef and therefore belongs to ACP-4,
		// where Workspace/Environment compatibility can be checked atomically.
		return fmt.Errorf("%w: resume lifecycle not enabled", adapter.ErrControlCapabilityUnsupported)
	default:
		return adapter.ErrControlCapabilityUnsupported
	}
}

// reconcileControlJournal makes the crash window explicit. A RECEIVED control
// command may have been executed just before a Worker crash, so it is marked
// UNKNOWN and is never automatically replayed.
func (w *Worker) reconcileControlJournal(ctx context.Context) error {
	rows, err := w.db.SQL.QueryContext(ctx,
		"SELECT id,body FROM commands WHERE kind='control' AND state='RECEIVED'")
	if err != nil {
		return err
	}
	type pending struct {
		id string
		body []byte
	}
	var all []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.body); err != nil {
			break
		}
		all = append(all, p)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, p := range all {
		cmd := new(pb.Command)
		if err = dec(p.body, cmd); err != nil {
			return err
		}
		c := cmd.GetControl()
		result := controlResult{
			CommandID: p.id, OperationID: c.GetOperationId(), Action: c.GetAction(),
			State: "UNKNOWN", ErrorCode: "WORKER_RESTARTED",
			Message: "control execution state is unknown after worker restart",
		}
		raw, _ := json.Marshal(result)
		if _, err = w.db.SQL.ExecContext(ctx,
			"UPDATE commands SET state='UNKNOWN',result=? WHERE id=? AND state='RECEIVED'",
			raw, p.id); err != nil {
			return err
		}
		var assignmentRaw []byte
		if err = w.db.SQL.QueryRowContext(ctx,
			"SELECT assignment FROM runs WHERE id=?", c.GetAttemptId()).Scan(&assignmentRaw); err == nil {
			a := new(pb.Assignment)
			if dec(assignmentRaw, a) == nil {
				_ = w.emit(ctx, a, "control.unknown", raw)
			}
		}
	}
	return nil
}
