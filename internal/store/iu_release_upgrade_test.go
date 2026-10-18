package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIUReleaseV20UpgradeAndUntouchedRollbackSnapshot(t *testing.T) {
	source := t.TempDir()
	db, e := Open(source, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	// Construct the actual v20 table boundary: remove all subsequently added
	// tables and restore v20's immutable Artifact ref deletion guard.
	if _, e = db.SQL.Exec(`DROP TRIGGER artifact_refs_delete_guard; CREATE TRIGGER artifact_refs_delete_guard BEFORE DELETE ON artifact_refs BEGIN SELECT RAISE(ABORT,'artifact references are immutable'); END;`); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"iu_recovery_publications", "iu_recovery_proposals", "iu_coverage_receipts", "iu_record_verifications", "iu_source_reads", "iu_node_reuse", "iu_reuse_offers", "iu_import_snapshots", "iu_import_grant_state", "iu_import_grants", "iu_validity_views", "iu_validity_progress", "iu_validity_events", "iu_validity_revisions"} {
		if _, e = db.SQL.Exec("DROP TABLE " + table); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.SQL.Exec(`INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism) VALUES('v20-job','owner','p','key','h','h','{}','single','SUCCEEDED',1,1,9999999999999,1);PRAGMA user_version=20;`); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(source, "artifact-marker"), []byte("v20 artifact evidence"), 0600); e != nil {
		t.Fatal(e)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	copyReleaseFixture(t, source, snapshot)
	before, e := os.ReadFile(filepath.Join(snapshot, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	db, e = Open(source, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	var version, count int
	if e = db.SQL.QueryRow(`PRAGMA user_version`).Scan(&version); e != nil || version != ServerVersion {
		t.Fatal(version, e)
	}
	if e = db.SQL.QueryRow(`SELECT count(*) FROM jobs WHERE id='v20-job' AND state='SUCCEEDED'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("history changed", e)
	}
	for _, table := range []string{"iu_recovery_publications", "iu_recovery_proposals", "iu_coverage_receipts", "iu_record_verifications", "iu_source_reads", "iu_node_reuse", "iu_reuse_offers", "iu_import_snapshots", "iu_validity_events"} {
		if e = db.SQL.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); e != nil || count != 1 {
			t.Fatal(table, e)
		}
	}
	rows, e := db.SQL.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	if rows.Next() {
		t.Fatal("broken FK")
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(filepath.Join(snapshot, "state.db"))
	if e != nil || Hash(before) != Hash(after) {
		t.Fatal("upgrade mutated rollback snapshot", e)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	copyReleaseFixture(t, snapshot, restored)
	db, e = OpenForBackup(restored)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.SQL.QueryRow(`PRAGMA user_version`).Scan(&version); e != nil || version != 20 {
		t.Fatal("rollback upgraded original snapshot", version, e)
	}
	if body, e := os.ReadFile(filepath.Join(restored, "artifact-marker")); e != nil || string(body) != "v20 artifact evidence" {
		t.Fatal("rollback lost artifact", e)
	}
}
