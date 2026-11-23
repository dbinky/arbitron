-- Restore migration 039's CHECK; source_logged_out rows cannot be downgraded.
CREATE TABLE notifications_old (
    id            TEXT PRIMARY KEY,
    instance_id   TEXT REFERENCES instances(id),
    type          TEXT NOT NULL CHECK (type IN (
                      'needs_interaction', 'failed', 'completed',
                      'schedule_skipped', 'schedule_missed',
                      'conflict_resolution', 'profile_refuses_triggers')),
    workflow_name TEXT NOT NULL DEFAULT '',
    read          INTEGER NOT NULL DEFAULT 0,
    dismissed     INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

INSERT INTO notifications_old (id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at)
SELECT id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at
FROM notifications
WHERE type <> 'source_logged_out';

DROP TABLE notifications;
ALTER TABLE notifications_old RENAME TO notifications;

CREATE INDEX IF NOT EXISTS idx_notifications_active
    ON notifications (read, dismissed, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_instance
    ON notifications (instance_id);

ALTER TABLE node_executions DROP COLUMN placement_history;
ALTER TABLE node_executions DROP COLUMN placement_reason;
ALTER TABLE node_executions DROP COLUMN source_id;
ALTER TABLE node_executions DROP COLUMN runner_id;
