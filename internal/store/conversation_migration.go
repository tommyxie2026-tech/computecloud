package store

const serverV17 = `
CREATE TABLE conversation_requests (
 request_id TEXT PRIMARY KEY,
 owner TEXT NOT NULL,
 profile_id TEXT NOT NULL,
 protocol TEXT NOT NULL CHECK(protocol IN ('messages','responses')),
 idem_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
	profile_hash TEXT NOT NULL,
 request_blob BLOB NOT NULL,
 job_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('PENDING','SUBMITTED','COMPLETED','FAILED','CANCELLED')),
 created INTEGER NOT NULL,
 updated INTEGER NOT NULL,
 UNIQUE(owner,profile_id,idem_key)
);
CREATE INDEX conversation_requests_job ON conversation_requests(job_id);
CREATE TRIGGER conversation_request_identity_immutable BEFORE UPDATE OF request_id,owner,profile_id,protocol,idem_key,request_hash,profile_hash,request_blob ON conversation_requests
BEGIN SELECT RAISE(ABORT,'conversation request identity immutable'); END;
`
