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
	if v != 7 {
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
	if e = db.SQL.QueryRow("PRAGMA user_version").Scan(&v); e != nil || v != 7 {
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
	if e = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 7 {
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


func TestV6ArtifactLifecycleSchema(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.SQL.Exec("INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline) VALUES('art-task','o','p','art','h','{}','SUCCEEDED',1,1,9999999999999)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("INSERT INTO artifacts(id,task,attempt,kind,hash,size,path,generation,state,created,updated) VALUES('art','art-task','attempt','result-bundle','hash',1,'art',1,'ACCEPTED',1,1)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("INSERT INTO artifact_refs(artifact,ref_type,ref_id,created) VALUES('art','task_result','art-task',1)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE artifacts SET state='ORPHANED' WHERE id='art'"); e == nil {
		t.Fatal("referenced ACCEPTED artifact left accepted state")
	}
	if _, e = db.SQL.Exec("DELETE FROM artifact_refs WHERE artifact='art'"); e == nil {
		t.Fatal("immutable artifact reference was deleted")
	}
	if _, e = db.SQL.Exec("INSERT INTO artifacts(id,task,attempt,kind,hash,size,path,generation,state,created,updated) VALUES('orphan','art-task','attempt','result-bundle','hash2',1,'orphan',1,'STAGED',1,1)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE artifacts SET state='DELETING' WHERE id='orphan'"); e == nil {
		t.Fatal("invalid STAGED -> DELETING transition accepted")
	}
	if _, e = db.SQL.Exec("UPDATE artifacts SET state='ORPHANED' WHERE id='orphan'"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE artifacts SET state='DELETING' WHERE id='orphan'"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE artifacts SET state='DELETED',path='',deleted_at=2 WHERE id='orphan'"); e != nil {
		t.Fatal(e)
	}
}


func TestV5ToV6BackfillsFrozenArtifactReferences(t *testing.T) {
	dir := t.TempDir()
	raw, e := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(ServerSchema); e != nil {
		t.Fatal(e)
	}
	for _, migration := range []string{serverV2, serverV3, serverV4, serverV5} {
		if _, e = raw.Exec(migration); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = raw.Exec("PRAGMA user_version=5"); e != nil {
		t.Fatal(e)
	}

	jobInsert := "INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism,manifest_json,manifest_hash,result_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	if _, e = raw.Exec(jobInsert, "jm", "o", "p", "jm", "rh1", "sh1", "{}", "map_reduce", "REDUCING", 1, 1, int64(9999999999999), 1,
		`{"version":"inputs.v1","items":[{"artifact_id":"ar-map"}]}`, "manifest-hash", "{}"); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(jobInsert, "jf", "o", "p", "jf", "rh2", "sh2", "{}", "single", "SUCCEEDED", 1, 1, int64(9999999999999), 1,
		"{}", "", `{"final_artifacts":[{"artifact_id":"ar-final"}]}`); e != nil {
		t.Fatal(e)
	}

	taskInsert := "INSERT INTO tasks(id,owner,project,idem,hash,spec,state,attempt,worker,created,updated,deadline,job_id,stage,partition_key,current_generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	if _, e = raw.Exec(taskInsert, "tm", "o", "p", "tm", "h1", "{}", "SUCCEEDED", "am", "w", 1, 1, int64(9999999999999), "jm", "map", "a", 1); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(taskInsert, "tf", "o", "p", "tf", "h2", "{}", "SUCCEEDED", "af", "w", 1, 1, int64(9999999999999), "jf", "single", "_single", 1); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released) VALUES('am','tm','w','e',1,'tm-token',1,1),('af','tf','w','e',1,'tf-token',1,1)"); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO artifacts(id,task,attempt,kind,hash,size,path,generation,state) VALUES('ar-map','tm','am','result-bundle','hm',1,'ar-map',1,'ACCEPTED'),('ar-final','tf','af','result-bundle','hf',1,'ar-final',1,'ACCEPTED')"); e != nil {
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

	for _, tc := range []struct {
		artifact, typ, ref string
	}{
		{"ar-map", "task_result", "tm"},
		{"ar-map", "reduce_input", "jm:reduce"},
		{"ar-final", "task_result", "tf"},
		{"ar-final", "job_result", "jf"},
	} {
		var n int
		if e = db.SQL.QueryRow("SELECT count(*) FROM artifact_refs WHERE artifact=? AND ref_type=? AND ref_id=?", tc.artifact, tc.typ, tc.ref).Scan(&n); e != nil || n != 1 {
			t.Fatalf("missing migrated ref %+v count=%d err=%v", tc, n, e)
		}
	}
}


func TestWorkerV3WorkspaceLifecycleSchema(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, WorkerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()

	var version int
	if e = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 3 {
		t.Fatalf("worker version=%d err=%v", version, e)
	}
	if _, e = db.SQL.Exec("INSERT INTO runs(id,assignment,state) VALUES('a','{}','DONE')"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec(`INSERT INTO workspaces(attempt,task,generation,repository_ref,base_commit,path,state,created,updated)
		VALUES('a','t',1,'repo','commit','a','PREPARING',1,1)`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE workspaces SET state='IN_USE' WHERE attempt='a'"); e == nil {
		t.Fatal("PREPARING -> IN_USE transition accepted")
	}
	if _, e = db.SQL.Exec("UPDATE workspaces SET state='READY' WHERE attempt='a'"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE workspaces SET task='other' WHERE attempt='a'"); e == nil {
		t.Fatal("workspace ownership mutation accepted")
	}
	if _, e = db.SQL.Exec("UPDATE workspaces SET state='IN_USE' WHERE attempt='a'"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE workspaces SET state='RETAINED',retain_until=2 WHERE attempt='a'"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE workspaces SET state='DELETING' WHERE attempt='a'"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("UPDATE workspaces SET state='DELETED',deleted_at=3 WHERE attempt='a'"); e != nil {
		t.Fatal(e)
	}
}


func TestV7LongRunningSchema(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()

	if _, e = db.SQL.Exec("INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline) VALUES('long','o','p','long','h','{}','RUNNING',1,1,9999999999999)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released,last_renewed) VALUES('long-a','long','w','e',1,'tok',100,0,90)"); e != nil {
		t.Fatal(e)
	}
	var renewed, floor int64
	if e = db.SQL.QueryRow("SELECT last_renewed FROM attempts WHERE id='long-a'").Scan(&renewed); e != nil || renewed != 90 {
		t.Fatalf("last_renewed=%d err=%v", renewed, e)
	}
	if e = db.SQL.QueryRow("SELECT event_floor_seq FROM tasks WHERE id='long'").Scan(&floor); e != nil || floor != 0 {
		t.Fatalf("event_floor_seq=%d err=%v", floor, e)
	}
	if _, e = db.SQL.Exec("INSERT INTO event_dedup(attempt,worker_seq,hash) VALUES('long-a',1,'h1')"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec("INSERT INTO event_dedup(attempt,worker_seq,hash) VALUES('long-a',1,'h2')"); e == nil {
		t.Fatal("duplicate worker event sequence accepted")
	}
}
