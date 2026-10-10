package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the published v0.4.6 Server schema as the source of the next
// release, then prove rollback uses an untouched pre-upgrade snapshot.
func TestReleaseV13ToV17AndSnapshotRestore(t *testing.T) {
	source := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(source, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{ServerSchema, serverV2, serverV3, serverV4, serverV5, serverV6, serverV7, serverV8, serverV9, serverV10, serverV11, serverV12, serverV13} {
		if _, err = raw.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.Exec(`INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism)
		VALUES('release-job','owner','project','release-key','hash','hash','{}','single','SUCCEEDED',1,2,1000,1);
		PRAGMA user_version=13`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "artifact-marker"), []byte("pre-upgrade"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	copyReleaseFixture(t, source, snapshot)

	upgraded, err := Open(source, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	var version, count int
	if err = upgraded.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatalf("upgraded version=%d err=%v", version, err)
	}
	if err = upgraded.SQL.QueryRow("SELECT count(*) FROM jobs WHERE id='release-job' AND state='SUCCEEDED'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical job count=%d err=%v", count, err)
	}
	for _, table := range []string{"goal_job_bindings", "goal_plans", "goal_governance_decisions", "goal_usage", "conversation_requests"} {
		if err = upgraded.SQL.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("new table %s count=%d err=%v", table, count, err)
		}
	}
	var integrity string
	if err = upgraded.SQL.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	foreignKeys, err := upgraded.SQL.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	violated := foreignKeys.Next()
	err = foreignKeys.Err()
	foreignKeys.Close()
	if err != nil || violated {
		t.Fatalf("foreign key check failed: violated=%t err=%v", violated, err)
	}
	if err = upgraded.Close(); err != nil {
		t.Fatal(err)
	}

	restored := filepath.Join(t.TempDir(), "restored")
	copyReleaseFixture(t, snapshot, restored)
	previous, err := OpenForBackup(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	if err = previous.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 13 {
		t.Fatalf("restored version=%d err=%v", version, err)
	}
	if err = previous.SQL.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("restored integrity=%q err=%v", integrity, err)
	}
	if err = previous.SQL.QueryRow("SELECT count(*) FROM jobs WHERE id='release-job'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored job count=%d err=%v", count, err)
	}
	if err = previous.SQL.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='goal_plans'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("restored release schema contaminated: %d %v", count, err)
	}
	if body, err := os.ReadFile(filepath.Join(restored, "artifact-marker")); err != nil || string(body) != "pre-upgrade" {
		t.Fatalf("restored artifact=%q err=%v", body, err)
	}
}

func copyReleaseFixture(t *testing.T, source, target string) {
	t.Helper()
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "artifact-marker"} {
		body, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(target, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
