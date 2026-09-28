package worker

import (
	"context"
	"errors"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	envreg "github.com/tommyxie2026-tech/computecloud/internal/environment"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func requiredEnvironment(a *pb.Assignment) (string, error) {
	if a == nil || a.Spec == nil {
		return "", errors.New("assignment task spec required")
	}
	name := envreg.RequiredName(a.Spec.RequiredCapabilities)
	if name == "" {
		return "", errors.New("assignment has conflicting environment requirements")
	}
	return name, nil
}

func (w *Worker) recordEnvironmentPrepared(ctx context.Context, a *pb.Assignment, ref envreg.Ref) error {
	name, err := requiredEnvironment(a)
	if err != nil {
		return err
	}
	if ref.Provider != name {
		return errors.New("environment provider mismatch")
	}
	body, err := envreg.EncodeRef(ref)
	if err != nil {
		return err
	}
	res, err := w.db.SQL.ExecContext(ctx, `UPDATE runs
		SET environment_provider=?,environment_ref=?,environment_state=?,environment_cleanup=?
		WHERE id=? AND completion IS NULL`,
		ref.Provider, body, string(envreg.StatePrepared), string(envreg.CleanupPending), a.AttemptId)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("environment prepare lost Attempt ownership")
	}
	return nil
}

func (w *Worker) recordEnvironmentActive(ctx context.Context, a *pb.Assignment, ref envreg.Ref) error {
	res, err := w.db.SQL.ExecContext(ctx, `UPDATE runs
		SET environment_state=?,environment_cleanup=?
		WHERE id=? AND environment_provider=? AND completion IS NULL`,
		string(envreg.StateActive), string(envreg.CleanupPending), a.AttemptId, ref.Provider)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("environment activation lost Attempt ownership")
	}
	return nil
}

func (w *Worker) recordEnvironmentTerminal(ctx context.Context, attempt string, result envreg.ReleaseResult) error {
	state := result.State
	if state == "" {
		state = envreg.StateUnknown
	}
	cleanup := result.Cleanup
	if cleanup == "" {
		cleanup = envreg.CleanupUnknown
	}
	_, err := w.db.SQL.ExecContext(ctx, `UPDATE runs
		SET environment_state=?,environment_cleanup=?
		WHERE id=? AND completion IS NULL`,
		string(state), string(cleanup), attempt)
	return err
}

func decodeEnvironmentRef(provider string, b []byte) (envreg.Ref, error) {
	ref, err := envreg.DecodeRef(b)
	if err != nil {
		return envreg.Ref{}, err
	}
	if provider == "" || ref.Provider != provider {
		return envreg.Ref{}, errors.New("environment provider mismatch")
	}
	return ref, nil
}

func environmentCleanupConfirmed(state envreg.CleanupState) bool {
	return state == envreg.CleanupConfirmed
}

func environmentRefQuery(ctx context.Context, q store.Query, attempt string) (string, []byte, error) {
	var provider string
	var ref []byte
	err := q.QueryRowContext(ctx, "SELECT environment_provider,environment_ref FROM runs WHERE id=?", attempt).Scan(&provider, &ref)
	return provider, ref, err
}


func (w *Worker) releaseEnvironment(ctx context.Context, attempt string, provider envreg.Provider, ref envreg.Ref) envreg.ReleaseResult {
	result, err := provider.Release(ctx, ref)
	if err != nil {
		result.Err = errors.Join(result.Err, err)
		if result.State == "" {
			result.State = envreg.StateUnknown
		}
		result.Cleanup = envreg.CleanupUnknown
	}
	if persistErr := w.recordEnvironmentTerminal(ctx, attempt, result); persistErr != nil {
		result.Err = errors.Join(result.Err, persistErr)
		result.State = envreg.StateUnknown
		result.Cleanup = envreg.CleanupUnknown
	}
	return result
}
