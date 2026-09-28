package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func (w *Worker) acceptControl(ctx context.Context, c *pb.Command) (bool, error) {
	cc := c.GetControl()
	if c.GetCommandId() == "" || c.GetKind() != "control" || cc == nil ||
		cc.OperationId == "" || cc.TaskId == "" || cc.AttemptId == "" ||
		cc.Generation < 1 || cc.LeaseToken == "" || !json.Valid(cc.PayloadJson) {
		return false, errors.New("invalid control command")
	}
	hash := store.Hash(enc(c))
	execute := false
	err := w.db.Tx(ctx, func(q store.Query) error {
		var old string
		err := q.QueryRowContext(ctx, "SELECT hash FROM commands WHERE id=?", c.CommandId).Scan(&old)
		if err == nil {
			if old != hash {
				return errors.New("command hash conflict")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var assignmentRaw []byte
		if err = q.QueryRowContext(ctx, "SELECT assignment FROM runs WHERE id=?", cc.AttemptId).Scan(&assignmentRaw); err != nil {
			return err
		}
		a := new(pb.Assignment)
		if err = dec(assignmentRaw, a); err != nil {
			return err
		}
		if a.TaskId != cc.TaskId || a.AttemptId != cc.AttemptId ||
			a.Generation != cc.Generation || a.LeaseToken != cc.LeaseToken {
			return errors.New("control attempt identity conflict")
		}
		if _, err = q.ExecContext(ctx, "INSERT INTO commands VALUES(?,?)", c.CommandId, hash); err != nil {
			return err
		}
		execute = true
		return nil
	})
	return execute, err
}

func controlCapabilityForOperation(operation string) control.Capability {
	switch operation {
	case "input":
		return control.CapabilityInteractiveInput
	case "interrupt":
		return control.CapabilityInterrupt
	case "approval":
		return control.CapabilityApproval
	case "resume":
		return control.CapabilitySessionResume
	default:
		return ""
	}
}

func (w *Worker) handleControl(ctx context.Context, cc *pb.ControlCommand) error {
	var assignmentRaw, runtimeRaw []byte
	var providerName string
	if err := w.db.SQL.QueryRowContext(ctx,
		"SELECT assignment,runtime_provider,runtime_ref FROM runs WHERE id=?",
		cc.AttemptId,
	).Scan(&assignmentRaw, &providerName, &runtimeRaw); err != nil {
		return err
	}
	a := new(pb.Assignment)
	if err := dec(assignmentRaw, a); err != nil {
		return err
	}
	provider, ok := adapter.Lookup(providerName)
	if !ok {
		return w.emitControlResult(ctx, a, cc, false, "RUNTIME_PROVIDER_MISSING")
	}
	sessionProvider, ok := provider.(adapter.SessionControlProvider)
	if !ok {
		return w.emitControlResult(ctx, a, cc, false, control.ErrorCapabilityUnsupported)
	}
	desc, claimed, err := adapter.ControlDescriptorFor(provider)
	if err != nil || !claimed {
		return w.emitControlResult(ctx, a, cc, false, control.ErrorCapabilityUnsupported)
	}
	required := controlCapabilityForOperation(cc.OperationType)
	found := false
	for _, capability := range desc.Capabilities {
		found = found || capability == required
	}
	if required == "" || !found {
		return w.emitControlResult(ctx, a, cc, false, control.ErrorCapabilityUnsupported)
	}
	ref, err := adapter.DecodeExecutionRef(runtimeRaw)
	if err != nil {
		return w.emitControlResult(ctx, a, cc, false, "RUNTIME_REF_UNAVAILABLE")
	}

	switch cc.OperationType {
	case "input":
		var payload struct {
			Mode    string `json:"mode"`
			Content string `json:"content"`
		}
		if err = json.Unmarshal(cc.PayloadJson, &payload); err == nil {
			err = sessionProvider.Input(ctx, adapter.ControlInputRequest{
				Ref: ref, SessionRef: cc.SessionRef, AttemptID: cc.AttemptId,
				Generation: cc.Generation, Mode: payload.Mode, Content: payload.Content,
			})
		}
	case "interrupt":
		err = sessionProvider.Interrupt(ctx, adapter.ControlInterruptRequest{
			Ref: ref, SessionRef: cc.SessionRef, AttemptID: cc.AttemptId, Generation: cc.Generation,
		})
	case "approval":
		var payload struct {
			ApprovalID     string `json:"approval_id"`
			RequestVersion int64  `json:"request_version"`
			Decision       string `json:"decision"`
		}
		if err = json.Unmarshal(cc.PayloadJson, &payload); err == nil {
			err = sessionProvider.Approve(ctx, adapter.ControlApprovalRequest{
				Ref: ref, SessionRef: cc.SessionRef, AttemptID: cc.AttemptId,
				Generation: cc.Generation, ApprovalID: payload.ApprovalID,
				RequestVersion: payload.RequestVersion, Decision: payload.Decision,
			})
		}
	case "resume":
		var resumed adapter.ExecutionRef
		resumed, err = sessionProvider.Resume(ctx, adapter.ControlResumeRequest{
			Ref: ref, SessionRef: cc.SessionRef, AttemptID: cc.AttemptId, Generation: cc.Generation,
		})
		if err == nil {
			var encoded []byte
			encoded, err = adapter.EncodeExecutionRef(resumed)
			if err == nil {
				_, err = w.db.SQL.ExecContext(ctx,
					"UPDATE runs SET runtime_provider=?,runtime_transport=?,runtime_ref=?,runtime_state=?,runtime_cleanup=? WHERE id=? AND completion IS NULL",
					resumed.Provider, resumed.Transport, encoded, string(adapter.RuntimeRunning),
					string(adapter.CleanupPending), cc.AttemptId)
			}
		}
	}
	if err != nil {
		return w.emitControlResult(ctx, a, cc, false, "RUNTIME_CONTROL_REJECTED")
	}
	return w.emitControlResult(ctx, a, cc, true, "")
}

func (w *Worker) emitControlResult(ctx context.Context, a *pb.Assignment, cc *pb.ControlCommand, accepted bool, code string) error {
	kind := "control.accepted"
	if !accepted {
		kind = "control.rejected"
	}
	return w.emit(ctx, a, kind, configJSON(map[string]any{
		"operation_id": cc.OperationId,
		"operation_type": cc.OperationType,
		"code": code,
	}))
}

func configJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
