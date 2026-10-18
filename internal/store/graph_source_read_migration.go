package store

const serverV24 = `
CREATE TABLE IF NOT EXISTS iu_source_reads (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,node_id TEXT NOT NULL,read_id TEXT NOT NULL,request BLOB NOT NULL,body BLOB NOT NULL,actor TEXT NOT NULL,created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,generation,node_id,read_id),FOREIGN KEY(goal_id,generation,node_id) REFERENCES iu_nodes(goal_id,generation,node_id)
);
CREATE TRIGGER IF NOT EXISTS iu_source_reads_immutable BEFORE UPDATE ON iu_source_reads BEGIN SELECT RAISE(ABORT,'source read receipts immutable'); END;
CREATE TRIGGER IF NOT EXISTS iu_source_reads_no_delete BEFORE DELETE ON iu_source_reads BEGIN SELECT RAISE(ABORT,'source read receipts immutable'); END;
`
