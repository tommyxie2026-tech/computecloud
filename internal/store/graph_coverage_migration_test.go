package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCoverageMigrationAtomicUpgradeAndSnapshotRollback(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"iu_recovery_publications", "iu_recovery_proposals", "iu_coverage_receipts"} {
		if _, e = db.SQL.Exec("DROP TABLE " + table); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.SQL.Exec(`PRAGMA user_version=25`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if e = os.WriteFile(filepath.Join(dir, "artifact-marker"), []byte("v25 evidence"), 0600); e != nil {
		t.Fatal(e)
	}
	snapshot := filepath.Join(t.TempDir(), "v25")
	copyReleaseFixture(t, dir, snapshot)
	before, e := os.ReadFile(filepath.Join(snapshot, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	db, e = Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	var v int
	if e = db.SQL.QueryRow(`PRAGMA user_version`).Scan(&v); e != nil || v != ServerVersion {
		t.Fatal(v, e)
	}
	for _, table := range []string{"iu_recovery_publications", "iu_recovery_proposals", "iu_coverage_receipts"} {
		var n int
		if e = db.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatal(table, n, e)
		}
	}
	db.Close()
	after, e := os.ReadFile(filepath.Join(snapshot, "state.db"))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("rollback snapshot changed", e)
	}
	old, e := OpenForBackup(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if e = old.SQL.QueryRow(`PRAGMA user_version`).Scan(&v); e != nil || v != 25 {
		t.Fatal("old snapshot migrated", v, e)
	}
}
