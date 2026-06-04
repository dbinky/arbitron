-- Per-user dismissal of terminal (completed/failed) instances. A row means the
-- user has dismissed that instance from their own queue view. It does not alter
-- or delete the instance. PRIMARY KEY (user_id, instance_id) makes dismiss
-- idempotent (INSERT ... ON CONFLICT DO NOTHING). Both foreign keys are
-- symmetric ON DELETE CASCADE so cleanup fires when either the instance OR the
-- user row is deleted (the Phase-6 account-deletion/erasure path), instead of a
-- hard FK failure (requires PRAGMA foreign_keys = ON).
CREATE TABLE IF NOT EXISTS instance_dismissals (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    dismissed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    PRIMARY KEY (user_id, instance_id)
);

CREATE INDEX IF NOT EXISTS idx_instance_dismissals_user
    ON instance_dismissals (user_id);
