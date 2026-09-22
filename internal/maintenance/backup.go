package maintenance

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

// Backup requires an offline, drained server, holds the same directory lock,
// closes SQLite before copying, and includes artifacts with the database.
func Backup(ctx context.Context, dir, out string) error {
	dir, e := filepath.Abs(dir)
	if e != nil {
		return e
	}
	out, e = filepath.Abs(out)
	if e != nil {
		return e
	}
	if rel, e := filepath.Rel(dir, out); e != nil || rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..") {
		return errors.New("backup output must be outside the data directory")
	}
	if _, e = os.Stat(filepath.Join(dir, "state.db")); e != nil {
		return e
	}
	d, e := store.Open(dir, store.ServerSchema)
	if e != nil {
		return e
	}
	defer d.Close()
	var n int
	if e = d.SQL.QueryRowContext(ctx, "SELECT count(*) FROM attempts WHERE released=0").Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return errors.New("backup requires drained attempts; inspect/cancel pending executions first")
	}
	var integrity string
	if e = d.SQL.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); e != nil {
		return e
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity check: %s", integrity)
	}
	if e = d.SQL.Close(); e != nil {
		return e
	}
	f, e := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(out)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	e = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		rel, e := filepath.Rel(dir, path)
		if e != nil {
			return e
		}
		if rel == "." || rel == "instance.lock" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup refuses symlink %s", rel)
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported backup file %s", rel)
		}
		h, e := tar.FileInfoHeader(info, "")
		if e != nil {
			return e
		}
		h.Name = filepath.ToSlash(rel)
		if e = tw.WriteHeader(h); e != nil {
			return e
		}
		if info.IsDir() {
			return nil
		}
		in, e := os.Open(path)
		if e != nil {
			return e
		}
		_, copyErr := io.Copy(tw, in)
		return errors.Join(copyErr, in.Close())
	})
	if e != nil {
		return e
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	ok = true
	return nil
}
