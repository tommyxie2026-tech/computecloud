package store

import (
	"testing"
)

func TestGraphMigrationSupportsNativeJobAndNodeStages(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec(`INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism) VALUES('graph','owner','p','key','h','h','{}','iu_graph','QUEUED',1,1,9999999999999,2)`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec(`INSERT INTO stages(id,job_id,kind,ordinal,state,created,updated) VALUES('graph:extract','graph','node:extract',0,'READY',1,1)`); e != nil {
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
	var n int
	if e = db.SQL.QueryRow(`SELECT count(*) FROM stages WHERE kind='node:extract'`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("reopen: %d %v", n, e)
	}
	rows, e := db.SQL.Query("PRAGMA foreign_key_check")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}
