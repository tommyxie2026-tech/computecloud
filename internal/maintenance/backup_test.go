package maintenance

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestOfflineBackupRestoresDatabaseAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	d, e := store.Open(dir, store.ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.SQL.Exec("INSERT INTO controls VALUES('task','control','digest')"); e != nil {
		t.Fatal(e)
	}
	d.Close()
	os.MkdirAll(filepath.Join(dir, "artifacts"), 0700)
	os.WriteFile(filepath.Join(dir, "artifacts", "result"), []byte("evidence"), 0600)
	out := filepath.Join(t.TempDir(), "backup.tar.gz")
	if e = Backup(context.Background(), dir, out); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(out)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	restored := t.TempDir()
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(restored, h.Name)
		if h.Typeflag == tar.TypeDir {
			os.MkdirAll(path, 0700)
			continue
		}
		os.MkdirAll(filepath.Dir(path), 0700)
		b, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	d, e = store.Open(restored, store.ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	var hash string
	if e = d.SQL.QueryRow("SELECT hash FROM controls").Scan(&hash); e != nil || hash != "digest" {
		t.Fatalf("restore database: %s %v", hash, e)
	}
	b, e := os.ReadFile(filepath.Join(restored, "artifacts", "result"))
	if e != nil || string(b) != "evidence" {
		t.Fatal("restore artifacts", e)
	}
}
