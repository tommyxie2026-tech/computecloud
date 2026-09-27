package worker

import (
	"context"
	"errors"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func (w *Worker) recordRuntimeStarted(ctx context.Context, a *pb.Assignment, ref adapter.ExecutionRef, version string) error {
	if a == nil || a.Spec == nil || ref.Provider != a.Spec.RuntimeProfile {
		return errors.New("runtime execution provider mismatch")
	}
	body, err := adapter.EncodeExecutionRef(ref)
	if err != nil {
		return err
	}
	res, err := w.db.SQL.ExecContext(ctx, `UPDATE runs
		SET state='RUNNING',pid=?,start_id=?,runtime_provider=?,runtime_transport=?,
		    runtime_ref=?,runtime_state=?,runtime_cleanup=?
		WHERE id=? AND completion IS NULL`,
		ref.PID, ref.StartID, ref.Provider, ref.Transport, body,
		string(adapter.RuntimeRunning), string(adapter.CleanupPending), a.AttemptId)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("runtime start lost Attempt ownership")
	}
	return w.emit(ctx, a, "attempt.started", config.JSON(map[string]any{
		"runtime_version": version,
		"runtime_provider": ref.Provider,
		"runtime_transport": ref.Transport,
		"runtime_ref": ref.ID,
		"workspace": a.AttemptId,
		"model": a.Spec.Model,
	}))
}

func (w *Worker) recordRuntimeTerminal(ctx context.Context, attempt string, result adapter.StartResult) error {
	if attempt == "" {
		return errors.New("attempt id required")
	}
	state := result.State
	if state == "" {
		state = adapter.RuntimeUnknown
	}
	cleanup := result.Cleanup
	if cleanup == "" {
		cleanup = adapter.CleanupUnknown
	}
	_, err := w.db.SQL.ExecContext(ctx, `UPDATE runs
		SET runtime_state=?,runtime_cleanup=?
		WHERE id=? AND completion IS NULL`,
		string(state), string(cleanup), attempt)
	return err
}

func decodeRuntimeRef(provider string, b []byte) (adapter.ExecutionRef, error) {
	ref, err := adapter.DecodeExecutionRef(b)
	if err != nil {
		return adapter.ExecutionRef{}, err
	}
	if provider == "" || ref.Provider != provider {
		return adapter.ExecutionRef{}, errors.New("runtime execution provider mismatch")
	}
	return ref, nil
}

func runtimeCleanupConfirmed(state adapter.CleanupState) bool {
	return state == adapter.CleanupConfirmed
}

func runtimeRefQuery(ctx context.Context, q store.Query, attempt string) (string, []byte, error) {
	var provider string
	var ref []byte
	err := q.QueryRowContext(ctx, "SELECT runtime_provider,runtime_ref FROM runs WHERE id=?", attempt).Scan(&provider, &ref)
	return provider, ref, err
}
