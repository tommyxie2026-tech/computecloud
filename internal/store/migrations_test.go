package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func seedV1(t *testing.T, dir string, active bool) {
	t.Helper()
	d, e := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if _, e = d.Exec(ServerSchema + "PRAGMA user_version=1;"); e != nil {
		t.Fatal(e)
	}
	if _, e = d.Exec("INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline) VALUES('old','owner','project','old','hash','{}','QUEUED',1,1,9999999999999)"); e != nil {
		t.Fatal(e)
	}
	if active {
		if _, e = d.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until) VALUES('attempt','old','worker','epoch',1,'token',9999999999999)"); e != nil {
			t.Fatal(e)
		}
	}
}
func TestV1UpgradeBackupAndDrainGate(t *testing.T) {
	dir := t.TempDir()
	seedV1(t, dir, false)
	backup, e := OpenForBackup(dir)
	if e != nil {
		t.Fatal(e)
	}
	var v int
	backup.SQL.QueryRow("PRAGMA user_version").Scan(&v)
	backup.Close()
	if v != 1 {
		t.Fatal("backup upgraded the source")
	}
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	db.SQL.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 4 {
		t.Fatalf("version %d", v)
	}
	var state, stage string
	var jid, stageID sql.NullString
	var generation int64
	if e = db.SQL.QueryRow("SELECT state,stage,job_id,stage_id,current_generation FROM tasks WHERE id='old'").Scan(&state, &stage, &jid, &stageID, &generation); e != nil || state != "QUEUED" || stage != "" || jid.Valid || stageID.Valid || generation != 0 {
		t.Fatalf("legacy row changed: %v state=%s stage=%s jid=%v stageID=%v gen=%d", e, state, stage, jid, stageID, generation)
	}
	db.Close()
	if wrong, e := Open(dir, WorkerSchema); e == nil {
		wrong.Close()
		t.Fatal("role mismatch accepted")
	}
	blocked := t.TempDir()
	seedV1(t, blocked, true)
	if d, e := Open(blocked, ServerSchema); e == nil {
		d.Close()
		t.Fatal("active migration accepted")
	}
	raw, _ := sql.Open("sqlite", filepath.Join(blocked, "state.db"))
	defer raw.Close()
	raw.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 1 {
		t.Fatal("failed upgrade advanced version")
	}
	var n int
	raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='jobs'").Scan(&n)
	if n != 0 {
		t.Fatal("drain refusal partially migrated")
	}
}
func TestIncrementalMigrationRollback(t *testing.T) {
	dir := t.TempDir()
	seedV1(t, dir, false)
	raw, e := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("CREATE TABLE gateway_requests(marker TEXT)"); e != nil {
		t.Fatal(e)
	}
	raw.Close()
	if d, e := Open(dir, ServerSchema); e == nil {
		d.Close()
		t.Fatal("conflicting migration accepted")
	}
	raw, _ = sql.Open("sqlite", filepath.Join(dir, "state.db"))
	defer raw.Close()
	var n, version int
	raw.QueryRow("PRAGMA user_version").Scan(&version)
	raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='jobs'").Scan(&n)
	if version != 1 || n != 0 {
		t.Fatalf("partial v2/v3 migration: version=%d jobs=%d", version, n)
	}
}


func TestV4MultiAttemptAndStageSchema(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var v int
	if e = db.SQL.QueryRow("PRAGMA user_version").Scan(&v); e != nil || v != 4 {
		t.Fatalf("version=%d err=%v", v, e)
	}
	// New schema must allow multiple historical attempts for one Task while
	// enforcing at most one unreleased attempt.
	if _, e = db.SQL.Exec("INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline,current_generation) VALUES('t','o','p','i','h','{}','QUEUED',1,1,9999999999999,1)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released) VALUES('a1','t','w','e1',1,'x',1,1)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released) VALUES('a2','t','w','e2',2,'y',9999999999999,0)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released) VALUES('a3','t','w','e3',3,'z',9999999999999,0)"); e == nil {
		t.Fatal("second active attempt accepted")
	}
}
