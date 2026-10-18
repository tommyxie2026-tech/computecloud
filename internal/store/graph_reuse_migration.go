package store

const serverV23 = `
CREATE TABLE IF NOT EXISTS iu_reuse_offers (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,node_id TEXT NOT NULL,grant_id TEXT NOT NULL,body BLOB NOT NULL,artifacts BLOB NOT NULL,
 PRIMARY KEY(goal_id,generation,node_id),FOREIGN KEY(goal_id,generation,node_id) REFERENCES iu_nodes(goal_id,generation,node_id),FOREIGN KEY(goal_id,grant_id) REFERENCES iu_import_grants(goal_id,grant_id)
);
CREATE TRIGGER IF NOT EXISTS iu_reuse_offers_immutable BEFORE UPDATE ON iu_reuse_offers BEGIN SELECT RAISE(ABORT,'reuse offers immutable'); END;
CREATE TRIGGER IF NOT EXISTS iu_reuse_offers_no_delete BEFORE DELETE ON iu_reuse_offers BEGIN SELECT RAISE(ABORT,'reuse offers immutable'); END;
CREATE TABLE IF NOT EXISTS iu_node_reuse (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,node_id TEXT NOT NULL,body BLOB NOT NULL,created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,generation,node_id),FOREIGN KEY(goal_id,generation,node_id) REFERENCES iu_nodes(goal_id,generation,node_id)
);
CREATE TRIGGER IF NOT EXISTS iu_node_reuse_immutable BEFORE UPDATE ON iu_node_reuse BEGIN SELECT RAISE(ABORT,'reuse decisions immutable'); END;
CREATE TRIGGER IF NOT EXISTS iu_node_reuse_no_delete BEFORE DELETE ON iu_node_reuse BEGIN SELECT RAISE(ABORT,'reuse decisions immutable'); END;
`
