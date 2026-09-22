package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestSQLiteDurabilityLockAndRollback(t *testing.T) {
	dir := t.TempDir()
	d, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(dir, ServerSchema); e == nil {
		second.Close()
		t.Fatal("second instance acquired lock")
	}
	for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1"} {
		var got string
		if e = d.SQL.QueryRow("PRAGMA " + pragma).Scan(&got); e != nil || got != want {
			t.Fatalf("%s=%s, %v", pragma, got, e)
		}
	}
	var version string
	if e = d.SQL.QueryRow("SELECT sqlite_version()").Scan(&version); e != nil {
		t.Fatal(e)
	}
	t.Log("embedded SQLite:", version)
	e = d.Tx(context.Background(), func(q Query) error {
		_, e := q.ExecContext(context.Background(), "INSERT INTO controls VALUES('t','c','hash')")
		if e != nil {
			return e
		}
		return errors.New("rollback")
	})
	if e == nil {
		t.Fatal("expected rollback")
	}
	var n int
	_ = d.SQL.QueryRow("SELECT count(*) FROM controls").Scan(&n)
	if n != 0 {
		t.Fatal("partial transaction persisted")
	}
	_, e = d.SQL.Exec("INSERT INTO controls VALUES('t','c','hash')")
	if e != nil {
		t.Fatal(e)
	}
	if e = d.Close(); e != nil {
		t.Fatal(e)
	}
	d, e = Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	_ = d.SQL.QueryRow("SELECT count(*) FROM controls").Scan(&n)
	if n != 1 {
		t.Fatal("committed data not recovered")
	}
}
func TestMigrationFailureAtomicAndFutureSchema(t *testing.T) {
	dir := t.TempDir()
	d, e := Open(dir, "CREATE TABLE must_rollback(id INTEGER); THIS IS INVALID SQL;")
	if e == nil {
		d.Close()
		t.Fatal("invalid migration succeeded")
	}
	raw, e := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	var n int
	if e = raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='must_rollback'").Scan(&n); e != nil || n != 0 {
		t.Fatalf("migration not atomic: %d %v", n, e)
	}
	_, e = raw.Exec("PRAGMA user_version=99")
	if e != nil {
		t.Fatal(e)
	}
	raw.Close()
	if d, e = Open(dir, ServerSchema); e == nil {
		d.Close()
		t.Fatal("future schema accepted")
	}
}
