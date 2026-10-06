package store

const serverV16 = `
CREATE TABLE goal_governance_decisions (
 goal_id TEXT NOT NULL REFERENCES goals(id), operation_id TEXT NOT NULL,
 request_hash TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
 reason TEXT NOT NULL, expected_version INTEGER NOT NULL,
 plan_revision INTEGER NOT NULL, graph_generation INTEGER NOT NULL,
 change_json BLOB NOT NULL, resulting_version INTEGER NOT NULL,
 created INTEGER NOT NULL, PRIMARY KEY(goal_id,operation_id)
);
CREATE TRIGGER goal_governance_immutable BEFORE UPDATE ON goal_governance_decisions
BEGIN SELECT RAISE(ABORT,'goal decision immutable'); END;
CREATE TABLE goal_usage (
 goal_id TEXT NOT NULL REFERENCES goals(id), usage_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL REFERENCES attempts(id), source TEXT NOT NULL,
 tokens INTEGER CHECK(tokens>=0), cost_units INTEGER CHECK(cost_units>=0),
 complete INTEGER NOT NULL CHECK(complete IN(0,1)), request_hash TEXT NOT NULL,
 created INTEGER NOT NULL, PRIMARY KEY(goal_id,usage_id),
 UNIQUE(attempt_id), CHECK(complete=0 OR (tokens IS NOT NULL AND cost_units IS NOT NULL))
);
CREATE TRIGGER goal_usage_immutable BEFORE UPDATE ON goal_usage
BEGIN SELECT RAISE(ABORT,'goal usage immutable'); END;
CREATE TABLE goal_budget_policy (
 goal_id TEXT PRIMARY KEY REFERENCES goals(id),
 max_tokens INTEGER CHECK(max_tokens>=1), max_cost_units INTEGER CHECK(max_cost_units>=1)
);
CREATE TABLE goal_replan_permissions (
 goal_id TEXT NOT NULL REFERENCES goals(id), operation_id TEXT NOT NULL,
 plan_revision INTEGER NOT NULL, graph_generation INTEGER NOT NULL,
 consumed INTEGER NOT NULL DEFAULT 0 CHECK(consumed IN(0,1)),
 PRIMARY KEY(goal_id,operation_id),
 FOREIGN KEY(goal_id,operation_id) REFERENCES goal_governance_decisions(goal_id,operation_id)
);
`
