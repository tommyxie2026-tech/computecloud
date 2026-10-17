package store

const serverV25 = `
CREATE TABLE IF NOT EXISTS iu_record_verifications (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,node_id TEXT NOT NULL,verification_id TEXT NOT NULL,record_id TEXT NOT NULL,scope TEXT NOT NULL,request BLOB NOT NULL,body BLOB NOT NULL,actor TEXT NOT NULL,created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,generation,node_id,verification_id),FOREIGN KEY(goal_id,generation,node_id) REFERENCES iu_nodes(goal_id,generation,node_id)
);
CREATE TRIGGER IF NOT EXISTS iu_record_verifications_immutable BEFORE UPDATE ON iu_record_verifications BEGIN SELECT RAISE(ABORT,'verification receipts immutable'); END;
CREATE TRIGGER IF NOT EXISTS iu_record_verifications_no_delete BEFORE DELETE ON iu_record_verifications BEGIN SELECT RAISE(ABORT,'verification receipts immutable'); END;
`
