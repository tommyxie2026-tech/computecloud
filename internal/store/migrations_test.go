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
	if v != 5 {
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


func TestV3ToV4PreservesJobAttemptArtifactAndGatewayReference(t *testing.T) {
	dir := t.TempDir()
	raw, e := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(ServerSchema); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(serverV2); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(serverV3); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("PRAGMA user_version=3"); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism,result_json) VALUES('j','o','p','i','rh','sh','{}','single','SUCCEEDED',1,2,9999999999999,1,'{}')"); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO tasks(id,owner,project,idem,hash,spec,state,attempt,worker,created,updated,deadline,job_id,stage,partition_key) VALUES('t','o','p','ti','h','{}','SUCCEEDED','a','w',1,2,9999999999999,'j','single','_single')"); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released,final_hash) VALUES('a','t','w','e',1,'tok',9999999999999,1,'done')"); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO artifacts(id,task,attempt,kind,hash,size,path) VALUES('ar','t','a','result-bundle','hash',1,'ar')"); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO gateway_requests(id,owner,project,job_id,attempt_id,route,model,endpoint,state,usage_complete,started) VALUES('g','o','p','j','a','r','m','/v1/responses','COMPLETE',0,1)"); e != nil {
		t.Fatal(e)
	}
	if e = raw.Close(); e != nil {
		t.Fatal(e)
	}

	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()

	var version int
	if e = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 5 {
		t.Fatalf("version=%d err=%v", version, e)
	}
	var stageID, stageState string
	if e = db.SQL.QueryRow("SELECT id,state FROM stages WHERE job_id='j' AND kind='single'").Scan(&stageID, &stageState); e != nil || stageState != "SUCCEEDED" {
		t.Fatalf("stage=%s state=%s err=%v", stageID, stageState, e)
	}
	var taskStage string
	var generation int64
	if e = db.SQL.QueryRow("SELECT stage_id,current_generation FROM tasks WHERE id='t'").Scan(&taskStage, &generation); e != nil || taskStage != stageID || generation != 1 {
		t.Fatalf("task stage=%s gen=%d err=%v", taskStage, generation, e)
	}
	var artifactGeneration int64
	var artifactState string
	if e = db.SQL.QueryRow("SELECT generation,state FROM artifacts WHERE id='ar'").Scan(&artifactGeneration, &artifactState); e != nil || artifactGeneration != 1 || artifactState != "ACCEPTED" {
		t.Fatalf("artifact gen=%d state=%s err=%v", artifactGeneration, artifactState, e)
	}
	var attemptID string
	if e = db.SQL.QueryRow("SELECT attempt_id FROM gateway_requests WHERE id='g'").Scan(&attemptID); e != nil || attemptID != "a" {
		t.Fatalf("gateway attempt=%s err=%v", attemptID, e)
	}
	rows, e := db.SQL.Query("PRAGMA foreign_key_check")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after v3->v4 migration")
	}
}


func TestV5RetryBackoffColumn(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.SQL.Exec("INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline) VALUES('retry','o','p','retry','h','{}','QUEUED',1,1,9999999999999)"); e != nil {
		t.Fatal(e)
	}
	var retryAfter int64
	if e = db.SQL.QueryRow("SELECT retry_after FROM tasks WHERE id='retry'").Scan(&retryAfter); e != nil || retryAfter != 0 {
		t.Fatalf("retry_after=%d err=%v", retryAfter, e)
	}
}
