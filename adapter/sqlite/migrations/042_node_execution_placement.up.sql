-- Remote runners (RR-16): where each node-execution attempt ran.
ALTER TABLE node_executions ADD COLUMN runner_id TEXT;
ALTER TABLE node_executions ADD COLUMN source_id TEXT;
ALTER TABLE node_executions ADD COLUMN placement_reason TEXT;
ALTER TABLE node_executions ADD COLUMN placement_history TEXT; -- JSON array of PlacementRecord

-- Admit source_logged_out (runner health monitor), preserving existing rows.
-- Safe with foreign_keys=ON because no table references notifications (see
-- migrations 013/039).
CREATE TABLE notifications_new (
    id            TEXT PRIMARY KEY,
    instance_id   TEXT REFERENCES instances(id),
    type          TEXT NOT NULL CHECK (type IN (
                      'needs_interaction', 'failed', 'completed',
                      'schedule_skipped', 'schedule_missed',
                      'conflict_resolution', 'profile_refuses_triggers',
                      'source_logged_out')),
    workflow_name TEXT NOT NULL DEFAULT '',
    read          INTEGER NOT NULL DEFAULT 0,
    dismissed     INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

INSERT INTO notifications_new (id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at)
SELECT id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;

CREATE INDEX IF NOT EXISTS idx_notifications_active
    ON notifications (read, dismissed, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_instance
    ON notifications (instance_id);
