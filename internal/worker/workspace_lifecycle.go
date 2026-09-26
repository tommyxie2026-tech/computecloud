package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/workspace"
)

const workspaceGCInterval = 30 * time.Second

func (w *Worker) workspaceRoot() string {
	return filepath.Join(w.cfg.DataDir, "workspaces")
}

func (w *Worker) workspaceRetention() time.Duration {
	if w.cfg.WorkspaceRetentionMS > 0 {
		return time.Duration(w.cfg.WorkspaceRetentionMS) * time.Millisecond
	}
	return 24 * time.Hour
}

func (w *Worker) prepareWorkspace(ctx context.Context, a *pb.Assignment) (string, error) {
	if a == nil || a.Spec == nil || a.Spec.Workspace == nil {
		return "", errors.New("workspace spec required")
	}
	repoRef := a.Spec.Workspace.RepositoryRef
	base := a.Spec.Workspace.BaseCommit
	source := w.cfg.Repositories[repoRef]
	if repoRef == "" || base == "" || source == "" {
		return "", errors.New("workspace repository/baseline unavailable")
	}
	root := w.workspaceRoot()
	target, err := workspace.Path(root, a.AttemptId)
	if err != nil {
		return "", err
	}
	now := store.Now()
	err = w.db.Tx(ctx, func(q store.Query) error {
		_, err := q.ExecContext(ctx, `INSERT INTO workspaces(
			attempt,task,generation,repository_ref,base_commit,path,state,created,updated
		) VALUES(?,?,?,?,?,?,'PREPARING',?,?) ON CONFLICT(attempt) DO NOTHING`,
			a.AttemptId, a.TaskId, a.Generation, repoRef, base, a.AttemptId, now, now)
		if err != nil {
			return err
		}
		var task, storedRepo, storedBase, path, state string
		var generation int64
		if err = q.QueryRowContext(ctx, `SELECT task,generation,repository_ref,base_commit,path,state
			FROM workspaces WHERE attempt=?`, a.AttemptId).
			Scan(&task, &generation, &storedRepo, &storedBase, &path, &state); err != nil {
			return err
		}
		if task != a.TaskId || generation != a.Generation || storedRepo != repoRef || storedBase != base || path != a.AttemptId {
			return errors.New("workspace ownership conflict")
		}
		if state != "PREPARING" {
			return fmt.Errorf("workspace already initialized in state %s", state)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	cwd, err := workspace.Prepare(ctx, root, a.AttemptId, source, base)
	if err != nil {
		_ = w.deleteWorkspace(ctx, a.AttemptId, "workspace prepare failed")
		return "", err
	}
	size, quotaErr := workspace.CheckQuota(cwd, w.cfg.WorkspaceMaxBytes)
	if quotaErr != nil {
		_ = w.db.Tx(context.Background(), func(q store.Query) error {
			_, e := q.ExecContext(context.Background(), "UPDATE workspaces SET size_bytes=?,updated=? WHERE attempt=? AND state='PREPARING'", size, store.Now(), a.AttemptId)
			return e
		})
		_ = w.deleteWorkspace(context.Background(), a.AttemptId, quotaErr.Error())
		return "", quotaErr
	}
	res, err := w.db.SQL.ExecContext(ctx, `UPDATE workspaces
		SET state='READY',size_bytes=?,updated=?,cleanup_error=''
		WHERE attempt=? AND state='PREPARING'`, size, store.Now(), a.AttemptId)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", err
	} else if n != 1 {
		return "", errors.New("workspace READY transition lost ownership")
	}
	if cwd != target {
		return "", errors.New("workspace path mismatch")
	}
	return cwd, nil
}

func (w *Worker) markWorkspaceInUse(ctx context.Context, a *pb.Assignment) error {
	res, err := w.db.SQL.ExecContext(ctx, `UPDATE workspaces
		SET state='IN_USE',updated=? WHERE attempt=? AND task=? AND generation=? AND state='READY'`,
		store.Now(), a.AttemptId, a.TaskId, a.Generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("workspace IN_USE transition lost ownership")
	}
	return nil
}

func (w *Worker) finalizeWorkspace(ctx context.Context, a *pb.Assignment, cleanupConfirmed bool) error {
	if a == nil {
		return nil
	}
	var state, path string
	err := w.db.SQL.QueryRowContext(ctx, "SELECT state,path FROM workspaces WHERE attempt=?", a.AttemptId).Scan(&state, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "RETAINED" || state == "QUARANTINED" || state == "DELETING" || state == "DELETED" {
		return nil
	}
	target, err := workspace.Path(w.workspaceRoot(), path)
	if err != nil {
		return err
	}
	size, sizeErr := workspace.Size(target)
	now := store.Now()
	if !cleanupConfirmed {
		msg := "execution cleanup unconfirmed"
		if sizeErr != nil {
			msg = sizeErr.Error()
		}
		_, err = w.db.SQL.ExecContext(ctx, `UPDATE workspaces
			SET state='QUARANTINED',updated=?,size_bytes=?,retain_until=0,cleanup_error=?
			WHERE attempt=? AND state IN ('PREPARING','READY','IN_USE')`,
			now, size, msg, a.AttemptId)
		return err
	}
	if sizeErr != nil && !errors.Is(sizeErr, os.ErrNotExist) {
		return sizeErr
	}
	_, err = w.db.SQL.ExecContext(ctx, `UPDATE workspaces
		SET state='RETAINED',updated=?,size_bytes=?,retain_until=?,cleanup_error=''
		WHERE attempt=? AND state IN ('PREPARING','READY','IN_USE')`,
		now, size, now+w.workspaceRetention().Milliseconds(), a.AttemptId)
	return err
}

func (w *Worker) workspacePreSpawnSafe(ctx context.Context, attempt string) bool {
	var state string
	if err := w.db.SQL.QueryRowContext(ctx, "SELECT state FROM workspaces WHERE attempt=?", attempt).Scan(&state); err != nil {
		return false
	}
	return state == "PREPARING" || state == "READY"
}

func (w *Worker) deleteWorkspace(ctx context.Context, attempt, reason string) error {
	now := store.Now()
	res, err := w.db.SQL.ExecContext(ctx, `UPDATE workspaces
		SET state='DELETING',updated=?,cleanup_error=?
		WHERE attempt=? AND state IN ('PREPARING','READY','RETAINED')`, now, reason, attempt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var state string
		if err = w.db.SQL.QueryRowContext(ctx, "SELECT state FROM workspaces WHERE attempt=?", attempt).Scan(&state); err != nil {
			return err
		}
		if state != "DELETING" && state != "DELETED" {
			return fmt.Errorf("workspace %s cannot be deleted from state %s", attempt, state)
		}
	}
	if err = workspace.Remove(w.workspaceRoot(), attempt); err != nil {
		_, _ = w.db.SQL.ExecContext(ctx, "UPDATE workspaces SET cleanup_error=?,updated=? WHERE attempt=? AND state='DELETING'", err.Error(), store.Now(), attempt)
		return err
	}
	_, err = w.db.SQL.ExecContext(ctx, `UPDATE workspaces
		SET state='DELETED',deleted_at=?,updated=?,cleanup_error=''
		WHERE attempt=? AND state='DELETING'`, store.Now(), store.Now(), attempt)
	return err
}

// bootstrapWorkspaceInventory adopts only legacy directories that can be tied
// to an existing local run. Unknown directories are deliberately left alone.
func (w *Worker) bootstrapWorkspaceInventory(ctx context.Context) error {
	root := w.workspaceRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		attempt := entry.Name()
		if _, err = workspace.Path(root, attempt); err != nil {
			continue
		}
		var count int
		if err = w.db.SQL.QueryRowContext(ctx, "SELECT count(*) FROM workspaces WHERE attempt=?", attempt).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			continue
		}
		var assignment, completion []byte
		var completed bool
		if err = w.db.SQL.QueryRowContext(ctx, "SELECT assignment,completion,completed FROM runs WHERE id=?", attempt).
			Scan(&assignment, &completion, &completed); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		a := new(pb.Assignment)
		if err = dec(assignment, a); err != nil || a.Spec == nil || a.Spec.Workspace == nil {
			continue
		}
		state := "QUARANTINED"
		retainUntil := int64(0)
		if len(completion) > 0 {
			done := new(pb.CompleteRequest)
			if dec(completion, done) == nil && done.CleanupConfirmed {
				state = "RETAINED"
				retainUntil = store.Now() + w.workspaceRetention().Milliseconds()
			}
		} else if completed {
			// A completed run without a durable completion record is unexpected;
			// quarantine instead of guessing that the workspace is safe to delete.
			state = "QUARANTINED"
		}
		size, _ := workspace.Size(filepath.Join(root, attempt))
		now := store.Now()
		if _, err = w.db.SQL.ExecContext(ctx, `INSERT INTO workspaces(
			attempt,task,generation,repository_ref,base_commit,path,state,created,updated,retain_until,size_bytes
		) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(attempt) DO NOTHING`,
			attempt, a.TaskId, a.Generation, a.Spec.Workspace.RepositoryRef, a.Spec.Workspace.BaseCommit,
			attempt, state, now, now, retainUntil, size); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) reconcileWorkspaceLifecycle(ctx context.Context) error {
	now := store.Now()
	if _, err := w.db.SQL.ExecContext(ctx, `UPDATE workspaces
		SET state='DELETING',updated=?
		WHERE attempt IN (
			SELECT w.attempt
			FROM workspaces w
			JOIN runs r ON r.id=w.attempt
			WHERE w.state='RETAINED'
			  AND w.retain_until>0
			  AND w.retain_until<=?
			  AND r.completed=1
			ORDER BY w.retain_until,w.attempt
			LIMIT 16
		)`, now, now); err != nil {
		return err
	}
	rows, err := w.db.SQL.QueryContext(ctx, "SELECT attempt FROM workspaces WHERE state='DELETING' ORDER BY updated,attempt LIMIT 16")
	if err != nil {
		return err
	}
	var attempts []string
	for rows.Next() {
		var attempt string
		if err = rows.Scan(&attempt); err != nil {
			break
		}
		attempts = append(attempts, attempt)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, attempt := range attempts {
		if err = w.deleteWorkspace(ctx, attempt, "retention expired"); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) workspaceLoop(ctx context.Context) {
	ticker := time.NewTicker(workspaceGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.reconcileWorkspaceLifecycle(ctx); err != nil && ctx.Err() == nil {
				// The regular Worker logger reports this from the caller's loop.
				// Keep lifecycle reconciliation retryable and non-fatal.
			}
		}
	}
}
