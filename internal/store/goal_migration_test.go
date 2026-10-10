package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestGoalV15MigrationRestartAndImmutableRecords(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct an empty v14 database by removing the v15-v17 migration tables.
	if _, err = db.SQL.Exec(`DROP TABLE conversation_requests;DROP TABLE goal_replan_permissions;DROP TABLE goal_governance_decisions;DROP TABLE goal_usage;DROP TABLE goal_budget_policy;DROP TABLE goal_attempt_reservations;DROP TABLE goal_evaluations;DROP TABLE goal_plans;DROP TABLE goal_job_bindings;PRAGMA user_version=14`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatalf("version=%d %v", version, err)
	}
	for _, name := range []string{"goal_job_bindings", "goal_plans", "goal_attempt_reservations", "goal_evaluations"} {
		var n int
		if err = db.SQL.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	db.Close()
	db, err = Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`INSERT INTO goals(id,owner,project,state,max_replans,max_total_attempts,max_wall_time_ms,created,updated) VALUES('g','o','p','RUNNING',0,1,1000,1,1);
 INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism) VALUES('j','o','p','k','h','h','{}','single','QUEUED',1,1,1000,1);
 INSERT INTO goal_plans VALUES('g',1,1,'j','{}','{}',1);`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("UPDATE goal_plans SET graph_json='{}' WHERE goal_id='g'"); err == nil {
		t.Fatal("immutable plan changed")
	}
}
