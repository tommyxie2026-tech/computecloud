package store

const serverV26 = `
CREATE TABLE iu_coverage_receipts(goal_id TEXT NOT NULL,generation INTEGER NOT NULL,receipt_id TEXT NOT NULL,request_hash TEXT NOT NULL,request BLOB NOT NULL,body BLOB NOT NULL,actor TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(goal_id,generation,receipt_id),FOREIGN KEY(goal_id,generation) REFERENCES iu_graphs(goal_id,generation));
CREATE TRIGGER iu_coverage_receipts_immutable BEFORE UPDATE ON iu_coverage_receipts BEGIN SELECT RAISE(ABORT,'coverage receipts immutable'); END;
CREATE TRIGGER iu_coverage_receipts_no_delete BEFORE DELETE ON iu_coverage_receipts BEGIN SELECT RAISE(ABORT,'coverage receipts immutable'); END;
CREATE TABLE iu_recovery_proposals(goal_id TEXT NOT NULL REFERENCES goals(id),proposal_id TEXT NOT NULL,request_hash TEXT NOT NULL,request BLOB NOT NULL,actor TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(goal_id,proposal_id));
CREATE TRIGGER iu_recovery_proposals_immutable BEFORE UPDATE ON iu_recovery_proposals BEGIN SELECT RAISE(ABORT,'recovery proposals immutable'); END;
CREATE TRIGGER iu_recovery_proposals_no_delete BEFORE DELETE ON iu_recovery_proposals BEGIN SELECT RAISE(ABORT,'recovery proposals immutable'); END;
CREATE TABLE iu_recovery_publications(goal_id TEXT NOT NULL,proposal_id TEXT NOT NULL,prior_generation INTEGER NOT NULL,next_generation INTEGER NOT NULL,job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id),created INTEGER NOT NULL,PRIMARY KEY(goal_id,proposal_id),UNIQUE(goal_id,prior_generation),FOREIGN KEY(goal_id,proposal_id) REFERENCES iu_recovery_proposals(goal_id,proposal_id),FOREIGN KEY(goal_id,next_generation) REFERENCES iu_graphs(goal_id,generation));
CREATE TRIGGER iu_recovery_publications_immutable BEFORE UPDATE ON iu_recovery_publications BEGIN SELECT RAISE(ABORT,'recovery publications immutable'); END;
CREATE TRIGGER iu_recovery_publications_no_delete BEFORE DELETE ON iu_recovery_publications BEGIN SELECT RAISE(ABORT,'recovery publications immutable'); END;
`
