package store

const serverV15 = `
CREATE TABLE goal_job_bindings (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id),
 goal_id TEXT NOT NULL REFERENCES goals(id),
 plan_revision INTEGER NOT NULL CHECK(plan_revision>=1),
 graph_generation INTEGER NOT NULL CHECK(graph_generation>=1),
 UNIQUE(goal_id,plan_revision),
 UNIQUE(goal_id,graph_generation)
);
CREATE TABLE goal_plans (
 goal_id TEXT NOT NULL REFERENCES goals(id),
 revision INTEGER NOT NULL,
 graph_generation INTEGER NOT NULL,
 job_id TEXT NOT NULL REFERENCES jobs(id),
 frozen_spec BLOB NOT NULL,
 graph_json BLOB NOT NULL,
 created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,revision)
);
CREATE TRIGGER goal_plans_immutable BEFORE UPDATE ON goal_plans
BEGIN SELECT RAISE(ABORT,'goal plan immutable'); END;
CREATE TABLE goal_attempt_reservations (
 attempt_id TEXT PRIMARY KEY REFERENCES attempts(id),
 goal_id TEXT NOT NULL REFERENCES goals(id),
 plan_revision INTEGER NOT NULL,
 graph_generation INTEGER NOT NULL,
 created INTEGER NOT NULL
);
CREATE INDEX goal_attempt_reservations_goal ON goal_attempt_reservations(goal_id);
CREATE TABLE goal_evaluations (
 goal_id TEXT NOT NULL REFERENCES goals(id),
 graph_generation INTEGER NOT NULL,
 job_id TEXT NOT NULL REFERENCES jobs(id),
 evaluator TEXT NOT NULL,
 verdict TEXT NOT NULL CHECK(verdict IN ('SUCCEEDED','FAILED','CANCELED')),
 evidence_json BLOB NOT NULL,
 created INTEGER NOT NULL,
 PRIMARY KEY(goal_id,graph_generation)
);
CREATE TRIGGER goal_evaluations_immutable BEFORE UPDATE ON goal_evaluations
BEGIN SELECT RAISE(ABORT,'goal evaluation immutable'); END;
`
