package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// extendGraphTable rebuilds a CHECK-constrained parent without rewriting child
// FK names. The caller disables foreign keys before BEGIN and checks them before commit.
func extendGraphTable(tx *sql.Tx, table, old, new string) error {
	var create string
	if e := tx.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&create); e != nil {
		return e
	}
	if !strings.Contains(create, old) {
		if strings.Contains(create, new) {
			return nil
		}
		return fmt.Errorf("unexpected %s CHECK definition", table)
	}
	rows, e := tx.Query("SELECT sql FROM sqlite_master WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL", table)
	if e != nil {
		return e
	}
	var auxiliaries []string
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			rows.Close()
			return e
		}
		auxiliaries = append(auxiliaries, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	temp := table + "_iu_v20"
	create = "CREATE TABLE " + temp + create[strings.Index(create, "("):]
	create = strings.Replace(create, old, new, 1)
	for _, stmt := range []string{create, "INSERT INTO " + temp + " SELECT * FROM " + table, "DROP TABLE " + table, "ALTER TABLE " + temp + " RENAME TO " + table} {
		if _, e = tx.Exec(stmt); e != nil {
			return e
		}
	}
	for _, stmt := range auxiliaries {
		if _, e = tx.Exec(stmt); e != nil {
			return e
		}
	}
	return nil
}

func migrateGraphV20(tx *sql.Tx) error {
	if e := extendGraphTable(tx, "jobs", "mode IN ('single','map_reduce')", "mode IN ('single','map_reduce','iu_graph')"); e != nil {
		return e
	}
	if e := extendGraphTable(tx, "stages", "kind IN ('single','map','reduce','verify')", "kind IN ('single','map','reduce','verify') OR (kind GLOB 'node:*' AND length(kind)<=69)"); e != nil {
		return e
	}
	_, e := tx.Exec(serverV20)
	return e
}

const serverV20 = `
CREATE TABLE IF NOT EXISTS iu_graphs (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id),
 goal_id TEXT NOT NULL REFERENCES goals(id),
 generation INTEGER NOT NULL CHECK(generation>=1),
 plan BLOB NOT NULL,
 output_hash TEXT NOT NULL DEFAULT '',
 evaluation_version INTEGER NOT NULL DEFAULT 0,
 evaluation_id TEXT NOT NULL DEFAULT '',
 UNIQUE(goal_id,generation)
);
CREATE TRIGGER IF NOT EXISTS iu_graph_frozen BEFORE UPDATE OF plan,goal_id,generation,job_id ON iu_graphs
BEGIN SELECT RAISE(ABORT,'IU graph is immutable'); END;
CREATE TABLE IF NOT EXISTS iu_nodes (
 goal_id TEXT NOT NULL,
 generation INTEGER NOT NULL,
 node_id TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'BLOCKED' CHECK(state IN ('BLOCKED','READY','RUNNING','SUCCEEDED','FAILED','SKIPPED','CANCELED')),
 task_id TEXT UNIQUE REFERENCES tasks(id),
 context_json BLOB,
 input_view BLOB,
 output_json BLOB,
 output_view BLOB,
 error_code TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(goal_id,generation,node_id),
 FOREIGN KEY(goal_id,generation) REFERENCES iu_graphs(goal_id,generation)
);
CREATE TABLE IF NOT EXISTS iu_admissions (
 goal_id TEXT NOT NULL,
 generation INTEGER NOT NULL,
 node_id TEXT NOT NULL,
 artifact_id TEXT NOT NULL REFERENCES artifacts(id),
 output_port TEXT NOT NULL,
 candidate_hash TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('PENDING','ADMITTED','REJECTED')),
 receipt_id TEXT NOT NULL UNIQUE,
 error_code TEXT NOT NULL DEFAULT '',
 retry_count INTEGER NOT NULL DEFAULT 0,
 next_retry_at INTEGER NOT NULL DEFAULT 0,
 view_json BLOB,
 created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,generation,node_id,artifact_id,output_port),
 FOREIGN KEY(goal_id,generation,node_id) REFERENCES iu_nodes(goal_id,generation,node_id)
);
CREATE TRIGGER IF NOT EXISTS iu_admission_terminal BEFORE UPDATE ON iu_admissions
WHEN OLD.state IN ('ADMITTED','REJECTED')
BEGIN SELECT RAISE(ABORT,'IU admission is terminal'); END;
CREATE TABLE IF NOT EXISTS iu_evaluations (
 id TEXT PRIMARY KEY,
 goal_id TEXT NOT NULL,
 generation INTEGER NOT NULL,
 version INTEGER NOT NULL,
 verdict TEXT NOT NULL CHECK(verdict IN ('PASS','FAIL','UNKNOWN')),
 body BLOB NOT NULL,
 created INTEGER NOT NULL,
 UNIQUE(goal_id,generation,version),
 FOREIGN KEY(goal_id,generation) REFERENCES iu_graphs(goal_id,generation)
);
CREATE TRIGGER IF NOT EXISTS iu_evaluation_immutable BEFORE UPDATE ON iu_evaluations
BEGIN SELECT RAISE(ABORT,'IU evaluation is immutable'); END;
`
