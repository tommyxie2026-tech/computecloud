package store

import (
	"database/sql"
	"errors"
	"fmt"
)

func migrateSchema(db *sql.DB, schema string, version, target int, migrate bool) error {
	if schema == ServerSchema || schema == WorkerSchema {
		other := "runs"
		if schema == WorkerSchema {
			other = "tasks"
		}
		var n int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", other).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return errors.New("data directory belongs to a different role")
		}
	}
	if !migrate {
		if version < 1 {
			return errors.New("no initialized database to back up")
		}
		return nil
	}
	if schema == ServerSchema && version > 0 && version < target {
		var active int
		if err := db.QueryRow("SELECT count(*) FROM attempts WHERE released=0").Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return errors.New("schema upgrade requires all attempts drained using the old binary")
		}
	}
	if version == target {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version == 0 {
		if _, err = tx.Exec(schema); err != nil {
			return err
		}
		version = 1
	}
	if schema == ServerSchema && version < 2 {
		if _, err = tx.Exec(serverV2); err != nil {
			return err
		}
		version = 2
	}
	if schema == ServerSchema && version < 3 {
		if _, err = tx.Exec(serverV3); err != nil {
			return err
		}
		version = 3
	}
	if schema == WorkerSchema && version < 2 {
		version = 2
	}
	rows, err := tx.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	bad := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return rowErr
	}
	if bad {
		return errors.New("foreign key check failed during migration")
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

const serverV2 = `CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  project TEXT NOT NULL,
  idem TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  spec_hash TEXT NOT NULL,
  spec BLOB NOT NULL,
  mode TEXT NOT NULL CHECK (mode IN ('single','map_reduce')),
  state TEXT NOT NULL CHECK (state IN
    ('QUEUED','EXECUTING','MAPPING','REDUCING','STOPPING','RECONCILING',
     'SUCCEEDED','FAILED','CANCELED')),
  version INTEGER NOT NULL DEFAULT 1,
  seq INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL,
  deadline INTEGER NOT NULL,
  parallelism INTEGER NOT NULL CHECK (parallelism BETWEEN 1 AND 8),
  stop_reason TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  manifest_json BLOB,
  manifest_hash TEXT NOT NULL DEFAULT '',
  result_json BLOB,
  UNIQUE(owner, project, idem)
);
CREATE INDEX jobs_active ON jobs(state, created);

ALTER TABLE tasks ADD COLUMN job_id TEXT REFERENCES jobs(id);
ALTER TABLE tasks ADD COLUMN stage TEXT NOT NULL DEFAULT ''
  CHECK (stage IN ('','single','map','reduce'));
ALTER TABLE tasks ADD COLUMN partition_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX tasks_job_partition
  ON tasks(job_id, stage, partition_key) WHERE job_id IS NOT NULL;
CREATE INDEX tasks_job_state ON tasks(job_id, stage, state);

CREATE TABLE job_events (
  job_id TEXT NOT NULL REFERENCES jobs(id),
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  recorded_at INTEGER NOT NULL,
  body BLOB NOT NULL,
  operation_key TEXT,
  operation_hash TEXT,
  PRIMARY KEY(job_id, seq),
  UNIQUE(job_id, operation_key),
  CHECK ((operation_key IS NULL AND operation_hash IS NULL) OR
         (operation_key IS NOT NULL AND operation_hash IS NOT NULL))
);`

const serverV3 = `ALTER TABLE attempts ADD COLUMN model_token_hash TEXT;
CREATE UNIQUE INDEX attempts_model_token
  ON attempts(model_token_hash) WHERE model_token_hash IS NOT NULL;

CREATE TABLE gateway_requests (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  project TEXT NOT NULL,
  job_id TEXT REFERENCES jobs(id),
  attempt_id TEXT REFERENCES attempts(id),
  route TEXT NOT NULL,
  model TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('STARTED','COMPLETE','FAILED','UNKNOWN')),
  upstream_request_id TEXT,
  input_tokens INTEGER,
  output_tokens INTEGER,
  usage_json BLOB,
  usage_complete INTEGER NOT NULL DEFAULT 0 CHECK (usage_complete IN (0,1)),
  started INTEGER NOT NULL,
  finished INTEGER,
  error_code TEXT NOT NULL DEFAULT ''
);
CREATE INDEX gateway_requests_job ON gateway_requests(job_id, started);`
