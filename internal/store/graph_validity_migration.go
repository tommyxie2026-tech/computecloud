package store

const serverV21 = `
CREATE TABLE IF NOT EXISTS iu_validity_revisions (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,revision INTEGER NOT NULL DEFAULT 0 CHECK(revision>=0),
 PRIMARY KEY(goal_id,generation),FOREIGN KEY(goal_id,generation) REFERENCES iu_graphs(goal_id,generation)
);
CREATE TABLE IF NOT EXISTS iu_validity_events (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,event_id TEXT NOT NULL,revision INTEGER NOT NULL,
 actor TEXT NOT NULL,body BLOB NOT NULL,audit_only INTEGER NOT NULL CHECK(audit_only IN(0,1)),created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,generation,event_id),UNIQUE(goal_id,generation,revision),
 FOREIGN KEY(goal_id,generation) REFERENCES iu_graphs(goal_id,generation)
);
CREATE TRIGGER IF NOT EXISTS iu_validity_events_immutable BEFORE UPDATE ON iu_validity_events BEGIN SELECT RAISE(ABORT,'validity events are immutable'); END;
CREATE TRIGGER IF NOT EXISTS iu_validity_events_no_delete BEFORE DELETE ON iu_validity_events BEGIN SELECT RAISE(ABORT,'validity events are immutable'); END;
CREATE TABLE IF NOT EXISTS iu_validity_progress (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,event_id TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN('RECORDED','APPLIED','AUDIT_ONLY')),
 PRIMARY KEY(goal_id,generation,event_id),FOREIGN KEY(goal_id,generation,event_id) REFERENCES iu_validity_events(goal_id,generation,event_id)
);
CREATE TABLE IF NOT EXISTS iu_validity_views (
 goal_id TEXT NOT NULL,generation INTEGER NOT NULL,node_id TEXT NOT NULL,revision INTEGER NOT NULL,
 original_hash TEXT NOT NULL,body BLOB NOT NULL,
 PRIMARY KEY(goal_id,generation,node_id,revision),FOREIGN KEY(goal_id,generation,node_id) REFERENCES iu_nodes(goal_id,generation,node_id)
);
CREATE TRIGGER IF NOT EXISTS iu_validity_views_immutable BEFORE UPDATE ON iu_validity_views BEGIN SELECT RAISE(ABORT,'validity snapshots are immutable'); END;
`
