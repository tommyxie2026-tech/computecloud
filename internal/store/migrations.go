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
	// SQLite table rebuilds of a referenced parent table require foreign-key
	// enforcement to be disabled before the transaction starts. v4 rebuilds
	// attempts in order to replace UNIQUE(task) with UNIQUE(task,generation);
	// gateway_requests may already reference attempts. Integrity is checked
	// explicitly before commit and enforcement is always restored afterwards.
	restoreFK := false
	if schema == ServerSchema && version < 4 {
		if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
			return err
		}
		restoreFK = true
		defer func() {
			_, _ = db.Exec("PRAGMA foreign_keys=ON")
		}()
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
	if schema == ServerSchema && version < 4 {
		if _, err = tx.Exec(serverV4); err != nil {
			return err
		}
		version = 4
	}
	if schema == ServerSchema && version < 5 {
		if _, err = tx.Exec(serverV5); err != nil {
			return err
		}
		version = 5
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
	if err = tx.Commit(); err != nil {
		return err
	}
	if restoreFK {
		if _, err = db.Exec("PRAGMA foreign_keys=ON"); err != nil {
			return err
		}
		restoreFK = false
		var enabled int
		if err = db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
			return err
		}
		if enabled != 1 {
			return errors.New("foreign key enforcement not restored after migration")
		}
	}
	return nil
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


const serverV4 = `CREATE TABLE stages (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES jobs(id),
  kind TEXT NOT NULL CHECK (kind IN ('single','map','reduce','verify')),
  ordinal INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('PENDING','READY','RUNNING','SUCCEEDED','FAILED','CANCELED')),
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL,
  UNIQUE(job_id, kind),
  UNIQUE(job_id, ordinal)
);
CREATE INDEX stages_job_state ON stages(job_id, state, ordinal);

INSERT INTO stages(id,job_id,kind,ordinal,state,created,updated)
SELECT id || ':single', id, 'single', 0,
       CASE
         WHEN state='SUCCEEDED' THEN 'SUCCEEDED'
         WHEN state='FAILED' THEN 'FAILED'
         WHEN state='CANCELED' THEN 'CANCELED'
         WHEN state='QUEUED' THEN 'READY'
         ELSE 'RUNNING'
       END,
       created, updated
FROM jobs WHERE mode='single';

INSERT INTO stages(id,job_id,kind,ordinal,state,created,updated)
SELECT id || ':map', id, 'map', 0,
       CASE
         WHEN state IN ('REDUCING','SUCCEEDED') THEN 'SUCCEEDED'
         WHEN state='FAILED' THEN 'FAILED'
         WHEN state='CANCELED' THEN 'CANCELED'
         WHEN state='QUEUED' THEN 'READY'
         ELSE 'RUNNING'
       END,
       created, updated
FROM jobs WHERE mode='map_reduce';

INSERT INTO stages(id,job_id,kind,ordinal,state,created,updated)
SELECT id || ':reduce', id, 'reduce', 1,
       CASE
         WHEN state='SUCCEEDED' THEN 'SUCCEEDED'
         WHEN state='FAILED' THEN 'FAILED'
         WHEN state='CANCELED' THEN 'CANCELED'
         WHEN state='REDUCING' THEN 'RUNNING'
         ELSE 'PENDING'
       END,
       created, updated
FROM jobs WHERE mode='map_reduce';

ALTER TABLE tasks ADD COLUMN stage_id TEXT REFERENCES stages(id);
ALTER TABLE tasks ADD COLUMN current_generation INTEGER NOT NULL DEFAULT 0;

UPDATE tasks
SET stage_id = job_id || ':' || stage
WHERE job_id IS NOT NULL AND stage IN ('single','map','reduce');

UPDATE tasks
SET current_generation = coalesce(
  (SELECT generation FROM attempts WHERE attempts.id=tasks.attempt),
  0
);

CREATE TABLE attempts_v4 (
  id TEXT PRIMARY KEY,
  task TEXT NOT NULL REFERENCES tasks(id),
  worker TEXT NOT NULL,
  epoch TEXT NOT NULL,
  generation INTEGER NOT NULL,
  token TEXT NOT NULL,
  lease_until INTEGER NOT NULL,
  released INTEGER NOT NULL DEFAULT 0,
  final_hash TEXT NOT NULL DEFAULT '',
  worker_seq INTEGER NOT NULL DEFAULT 0,
  model_token_hash TEXT,
  UNIQUE(task, generation)
);
INSERT INTO attempts_v4(id,task,worker,epoch,generation,token,lease_until,released,final_hash,worker_seq,model_token_hash)
SELECT id,task,worker,epoch,generation,token,lease_until,released,final_hash,worker_seq,model_token_hash
FROM attempts;
DROP TABLE attempts;
ALTER TABLE attempts_v4 RENAME TO attempts;
CREATE UNIQUE INDEX attempts_one_active ON attempts(task) WHERE released=0;
CREATE UNIQUE INDEX attempts_model_token ON attempts(model_token_hash) WHERE model_token_hash IS NOT NULL;
CREATE INDEX attempts_worker_active ON attempts(worker,released);

ALTER TABLE artifacts ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artifacts ADD COLUMN state TEXT NOT NULL DEFAULT 'STAGED'
  CHECK (state IN ('STAGED','ACCEPTED','ORPHANED'));

UPDATE artifacts
SET generation = coalesce((SELECT generation FROM attempts WHERE attempts.id=artifacts.attempt),0);

UPDATE artifacts
SET state = 'ACCEPTED'
WHERE attempt IN (
  SELECT a.id
  FROM attempts a
  JOIN tasks t ON t.id=a.task
  WHERE a.released=1 AND t.state='SUCCEEDED' AND t.attempt=a.id
);

UPDATE artifacts
SET state = 'ORPHANED'
WHERE state='STAGED' AND attempt IN (SELECT id FROM attempts WHERE released=1);
`


const serverV5 = `ALTER TABLE tasks ADD COLUMN retry_after INTEGER NOT NULL DEFAULT 0;
CREATE INDEX tasks_retry_queue ON tasks(state,retry_after,priority DESC,created);`
