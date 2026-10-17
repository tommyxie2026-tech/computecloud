package server

import (
	"archive/tar"
	"compress/gzip"
	"github.com/tommyxie2026-tech/computecloud/internal/maintenance"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIUBackupRestoresAcceptedImportWithSubmissionsDisabled(t *testing.T) {
	h, source, spec, ev := importSource(t)
	grant := makeImportGrant(t, h, source, spec)
	if _, e := h.s.IssueGraphImport(h.ctx, source.ID, grant); e != nil {
		t.Fatal(e)
	}
	next, e := publishImport(t, h, source, spec, ev)
	if e != nil {
		t.Fatal(e)
	}
	cfg := h.s.cfg
	if e = h.s.Close(); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(t.TempDir(), "iu-backup.tar.gz")
	if e = maintenance.Backup(h.ctx, cfg.DataDir, archive); e != nil {
		t.Fatal(e)
	}
	file, e := os.Open(archive)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	gz, e := gzip.NewReader(file)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	restored := t.TempDir()
	for {
		header, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		clean := filepath.Clean(header.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			t.Fatal("unsafe backup member")
		}
		path := filepath.Join(restored, clean)
		if header.Typeflag == tar.TypeDir {
			if e = os.MkdirAll(path, 0700); e != nil {
				t.Fatal(e)
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			t.Fatal("non-regular backup member")
		}
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(reader)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, body, 0600); e != nil {
			t.Fatal(e)
		}
	}
	cfg.DataDir = restored
	cfg.Jobs.GraphsEnabled = false
	h.s, e = New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	g, e := h.s.GraphImport(h.ctx, source.ID, grant.ID)
	if e != nil || g.State != "CONSUMED" || g.TargetJob != next.ID || len(g.Snapshots) != 1 {
		t.Fatalf("restored grant %+v %v", g, e)
	}
	if e = h.s.advanceGraph(h.ctx, next.ID); e != nil {
		t.Fatal(e)
	}
	var tasks, attempts int
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM tasks WHERE job_id=?`, next.ID).Scan(&tasks)
	h.s.db.SQL.QueryRow(`SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=?`, next.ID).Scan(&attempts)
	if tasks != 1 || attempts != 0 {
		t.Fatalf("restored activation duplicated execution tasks=%d attempts=%d", tasks, attempts)
	}
	var pins int
	if e = h.s.db.SQL.QueryRow(`SELECT count(*) FROM artifact_refs WHERE ref_id LIKE 'import/%'`).Scan(&pins); e != nil || pins < 1 {
		t.Fatal("restored pin missing", e)
	}
	if _, e = h.s.SubmitGraph(h.ctx, "disabled-new", nil); e == nil {
		t.Fatal("new graph accepted while disabled")
	}
}
