package store

const serverV22 = `
CREATE TABLE IF NOT EXISTS iu_import_grants (
 goal_id TEXT NOT NULL,grant_id TEXT NOT NULL,source_job TEXT NOT NULL,body BLOB NOT NULL,actor TEXT NOT NULL,issuer_digest TEXT NOT NULL,artifacts BLOB NOT NULL,created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,grant_id),FOREIGN KEY(goal_id) REFERENCES goals(id),FOREIGN KEY(source_job) REFERENCES jobs(id)
);
CREATE TRIGGER IF NOT EXISTS iu_import_grants_immutable BEFORE UPDATE ON iu_import_grants BEGIN SELECT RAISE(ABORT,'import grants immutable'); END;
CREATE TRIGGER IF NOT EXISTS iu_import_grants_no_delete BEFORE DELETE ON iu_import_grants BEGIN SELECT RAISE(ABORT,'import grants immutable'); END;
CREATE TABLE IF NOT EXISTS iu_import_grant_state (
 goal_id TEXT NOT NULL,grant_id TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN('ISSUED','CONSUMED','REVOKED','EXPIRED')),expires_at INTEGER NOT NULL,target_job TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(goal_id,grant_id),FOREIGN KEY(goal_id,grant_id) REFERENCES iu_import_grants(goal_id,grant_id)
);
CREATE TABLE IF NOT EXISTS iu_import_snapshots (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,node_id TEXT NOT NULL,grant_id TEXT NOT NULL,body BLOB NOT NULL,artifacts BLOB NOT NULL,
 PRIMARY KEY(goal_id,generation,node_id,grant_id),FOREIGN KEY(goal_id,generation,node_id) REFERENCES iu_nodes(goal_id,generation,node_id),FOREIGN KEY(goal_id,grant_id) REFERENCES iu_import_grants(goal_id,grant_id)
);
CREATE TRIGGER IF NOT EXISTS iu_import_snapshots_immutable BEFORE UPDATE ON iu_import_snapshots BEGIN SELECT RAISE(ABORT,'imports immutable'); END;
CREATE TRIGGER IF NOT EXISTS iu_import_snapshots_no_delete BEFORE DELETE ON iu_import_snapshots BEGIN SELECT RAISE(ABORT,'imports immutable'); END;
DROP TRIGGER IF EXISTS artifact_refs_delete_guard;
CREATE TRIGGER artifact_refs_delete_guard BEFORE DELETE ON artifact_refs
WHEN NOT (OLD.ref_type='iu_memory' AND EXISTS (
 SELECT 1 FROM iu_import_grant_state s WHERE OLD.ref_id='import-grant/'||s.goal_id||'/'||s.grant_id AND s.state IN('REVOKED','EXPIRED','CONSUMED')
)) BEGIN SELECT RAISE(ABORT,'artifact references are immutable'); END;
`
