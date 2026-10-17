package store

import "testing"

func TestGraphValidityMigrationPreservesV20Jobs(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec(`DROP TABLE iu_validity_views; DROP TABLE iu_validity_progress; DROP TABLE iu_validity_events; DROP TABLE iu_validity_revisions; PRAGMA user_version=20;
 INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism)
 VALUES('old-job','owner','p','old','h','h','{}','single','SUCCEEDED',1,1,9999999999999,1)`); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var v int
	var state string
	if e = db.SQL.QueryRow(`PRAGMA user_version`).Scan(&v); e != nil || v != ServerVersion {
		t.Fatalf("version %d %v", v, e)
	}
	if e = db.SQL.QueryRow(`SELECT state FROM jobs WHERE id='old-job'`).Scan(&state); e != nil || state != "SUCCEEDED" {
		t.Fatalf("legacy job %s %v", state, e)
	}
	for _, table := range []string{"iu_validity_revisions", "iu_validity_events", "iu_validity_progress", "iu_validity_views"} {
		var count int
		if e = db.SQL.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); e != nil || count != 1 {
			t.Fatalf("table %s %d %v", table, count, e)
		}
	}
	rows, e := db.SQL.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}
