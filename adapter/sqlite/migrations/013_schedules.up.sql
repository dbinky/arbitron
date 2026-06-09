-- Recurring schedules (org-readiness Phase 3). Each row starts a workflow on a
-- cron + timezone, attributed to its creator. id/creator_id are TEXT UUIDs to
-- match users.id and the existing TEXT-uuid convention (see published_workflows).
CREATE TABLE IF NOT EXISTS schedules (
    id            TEXT PRIMARY KEY,
    workflow_name TEXT NOT NULL,
    cron_expr     TEXT NOT NULL,
    timezone      TEXT NOT NULL DEFAULT 'UTC',
    creator_id    TEXT NOT NULL REFERENCES users(id),
    department    TEXT NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1,   -- 0/1 boolean
    variables     TEXT NOT NULL DEFAULT '{}',   -- JSON object of workflow inputs
    last_fired_at TEXT,                          -- NULL until first fire (RFC3339 UTC)
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

CREATE INDEX IF NOT EXISTS idx_schedules_creator       ON schedules(creator_id);
CREATE INDEX IF NOT EXISTS idx_schedules_workflow_name ON schedules(workflow_name);
-- ListEnabled is the evaluator's hot path: enabled rows ordered for a stable scan.
CREATE INDEX IF NOT EXISTS idx_schedules_enabled       ON schedules(enabled, last_fired_at);

-- Relax the notifications table so schedule alerts (schedule_skipped /
-- schedule_missed) can be persisted. These alerts have NO instance (a run did
-- not happen), so instance_id must be nullable, and the type CHECK constraint
-- must admit the two new schedule types. SQLite cannot ALTER a CHECK constraint
-- or a column's NOT NULL in place, so the table is rebuilt (data preserved).
--
-- WARNING (SQLite safe-rebuild assumption): this DROP/RENAME runs INSIDE the
-- migration transaction with PRAGMA foreign_keys=ON (see db.go). That is safe
-- TODAY only because nothing FK-references notifications. SQLite's documented
-- safe table-rebuild requires PRAGMA foreign_keys=OFF set OUTSIDE the
-- transaction (the pragma is a no-op mid-transaction). The day any table adds a
-- foreign key TO notifications, this pattern will silently corrupt or fail —
-- rebuild it per the SQLite "making other kinds of table schema changes"
-- procedure (foreign_keys=OFF outside the txn) instead of copying this block.
CREATE TABLE notifications_new (
    id          TEXT PRIMARY KEY,
    instance_id TEXT REFERENCES instances(id),   -- NULL for schedule alerts
    type        TEXT NOT NULL CHECK (type IN (
                    'needs_interaction', 'failed', 'completed',
                    'schedule_skipped', 'schedule_missed')),
    workflow_name TEXT NOT NULL DEFAULT '',       -- denormalized for instance-less alerts
    read        INTEGER NOT NULL DEFAULT 0,
    dismissed   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

INSERT INTO notifications_new (id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at)
SELECT id, instance_id, type, '', read, dismissed, created_at, updated_at FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;

CREATE INDEX IF NOT EXISTS idx_notifications_active
    ON notifications (read, dismissed, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_instance
    ON notifications (instance_id);
