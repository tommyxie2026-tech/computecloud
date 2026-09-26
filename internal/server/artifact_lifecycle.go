package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

const (
	artifactOrphanRetentionMS = int64((24 * time.Hour) / time.Millisecond)
	artifactUploadTempMaxAge  = time.Hour
	artifactSweepIntervalMS   = int64((30 * time.Second) / time.Millisecond)
)

func pinArtifactRef(ctx context.Context, q store.Query, artifact, refType, refID string) error {
	var state string
	if err := q.QueryRowContext(ctx, "SELECT state FROM artifacts WHERE id=?", artifact).Scan(&state); err != nil {
		return err
	}
	if state != "ACCEPTED" {
		return errors.New("artifact reference requires ACCEPTED artifact")
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO artifact_refs(artifact,ref_type,ref_id,created) VALUES(?,?,?,?) ON CONFLICT(artifact,ref_type,ref_id) DO NOTHING",
		artifact, refType, refID, store.Now())
	return err
}

func acceptArtifact(ctx context.Context, q store.Query, id, attempt, task string, generation int64) error {
	now := store.Now()
	res, err := q.ExecContext(ctx, "UPDATE artifacts SET state='ACCEPTED',updated=?,gc_after=0 WHERE id=? AND attempt=? AND generation=? AND state='STAGED'",
		now, id, attempt, generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("artifact acceptance lost generation ownership")
	}
	return pinArtifactRef(ctx, q, id, "task_result", task)
}

func orphanStagedArtifacts(ctx context.Context, q store.Query, attempt string, generation int64) error {
	now := store.Now()
	_, err := q.ExecContext(ctx, "UPDATE artifacts SET state='ORPHANED',updated=?,gc_after=? WHERE attempt=? AND generation=? AND state='STAGED'",
		now, now+artifactOrphanRetentionMS, attempt, generation)
	return err
}

func (s *Server) reconcileArtifactLifecycle(ctx context.Context) error {
	now := store.Now()
	if err := s.db.Tx(ctx, func(q store.Query) error {
		_, err := q.ExecContext(ctx, `UPDATE artifacts
			SET state='DELETING',updated=?
			WHERE id IN (
				SELECT a.id
				FROM artifacts a
				WHERE a.state='ORPHANED'
				  AND a.gc_after>0
				  AND a.gc_after<=?
				  AND NOT EXISTS (SELECT 1 FROM artifact_refs r WHERE r.artifact=a.id)
				ORDER BY a.gc_after,a.id
				LIMIT 32
			)`, now, now)
		return err
	}); err != nil {
		return err
	}

	rows, err := s.db.SQL.QueryContext(ctx, "SELECT id,path FROM artifacts WHERE state='DELETING' ORDER BY updated,id LIMIT 32")
	if err != nil {
		return err
	}
	type item struct{ id, path string }
	var items []item
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.path); err != nil {
			break
		}
		items = append(items, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}

	dir := filepath.Join(s.cfg.DataDir, "artifacts")
	for _, v := range items {
		if !artifactID.MatchString(v.path) {
			return errors.New("invalid artifact path during lifecycle reconciliation")
		}
		err = os.Remove(filepath.Join(dir, v.path))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		res, e := s.db.SQL.ExecContext(ctx, `UPDATE artifacts
			SET state='DELETED',path='',updated=?,deleted_at=?
			WHERE id=? AND state='DELETING'
			  AND NOT EXISTS (SELECT 1 FROM artifact_refs WHERE artifact=?)`, now, now, v.id, v.id)
		if e != nil {
			return e
		}
		if n, e := res.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return errors.New("artifact deletion lost lifecycle ownership")
		}
	}
	return s.cleanupArtifactFilesystem(ctx, now)
}

func (s *Server) cleanupArtifactFilesystem(ctx context.Context, now int64) error {
	dir := filepath.Join(s.cfg.DataDir, "artifacts")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			return err
		}
		age := time.Duration(now-info.ModTime().UnixMilli()) * time.Millisecond
		if strings.HasPrefix(name, ".upload-") {
			if age >= artifactUploadTempMaxAge {
				if err = os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			continue
		}
		if !artifactID.MatchString(name) || age < 24*time.Hour {
			continue
		}
		var n int
		if err = s.db.SQL.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE id=?", name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if err = os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
