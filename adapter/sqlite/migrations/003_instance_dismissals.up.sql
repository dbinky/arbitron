-- Per-user dismissal of terminal (completed/failed) instances. A row means the
-- user has dismissed that instance from their own queue view. It does not alter
-- or delete the instance. PRIMARY KEY (user_id, instance_id) makes dismiss
-- idempotent (INSERT ... ON CONFLICT DO NOTHING). ON DELETE CASCADE cleans up
-- if an instance row is ever deleted (requires PRAGMA foreign_keys = ON).
CREATE TABLE IF NOT EXISTS instance_dismissals (
    user_id      TEXT NOT NULL REFERENCES users(id),
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    dismissed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    PRIMARY KEY (user_id, instance_id)
);

CREATE INDEX IF NOT EXISTS idx_instance_dismissals_user
    ON instance_dismissals (user_id);
