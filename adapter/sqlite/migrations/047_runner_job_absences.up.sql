-- Decision D6: a remote runner reconnecting without a ledger-live job reports
-- durable stop evidence for that exact job generation.
CREATE TABLE runner_job_absences (
 job_id TEXT NOT NULL, generation INTEGER NOT NULL CHECK(generation >= 1),
 runner_id TEXT NOT NULL, reported_at TEXT NOT NULL,
 PRIMARY KEY (job_id, generation)
);

-- SQLite cannot alter a CHECK; rebuild while preserving earlier receipts.
CREATE TABLE workspace_lease_supersessions_047 (
 resource_key TEXT NOT NULL, old_generation INTEGER NOT NULL, old_token TEXT NOT NULL,
 new_generation INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('runner_lost','revoked','stopped','server_restart','reconnect_absent')),
 job_id TEXT, job_generation INTEGER, superseded_at TEXT NOT NULL,
 PRIMARY KEY (resource_key, old_generation)
);
INSERT INTO workspace_lease_supersessions_047 (resource_key, old_generation, old_token, new_generation, kind, job_id, job_generation, superseded_at)
 SELECT resource_key, old_generation, old_token, new_generation, kind, job_id, job_generation, superseded_at FROM workspace_lease_supersessions;
DROP TABLE workspace_lease_supersessions;
ALTER TABLE workspace_lease_supersessions_047 RENAME TO workspace_lease_supersessions;
