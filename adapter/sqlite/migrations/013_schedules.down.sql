DROP INDEX IF EXISTS idx_schedules_enabled;
DROP INDEX IF EXISTS idx_schedules_workflow_name;
DROP INDEX IF EXISTS idx_schedules_creator;
DROP TABLE IF EXISTS schedules;

-- Restore the notifications table to its pre-013 shape (NOT NULL instance_id,
-- the original three-value type CHECK, no denormalized workflow_name). Rows that
-- carry a NULL instance_id (schedule alerts) cannot be represented downgraded
-- and are dropped.
DROP INDEX IF EXISTS idx_notifications_instance;
DROP INDEX IF EXISTS idx_notifications_active;

CREATE TABLE notifications_old (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES instances(id),
    type        TEXT NOT NULL CHECK (type IN ('needs_interaction', 'failed', 'completed')),
    read        INTEGER NOT NULL DEFAULT 0,
    dismissed   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

INSERT INTO notifications_old (id, instance_id, type, read, dismissed, created_at, updated_at)
SELECT id, instance_id, type, read, dismissed, created_at, updated_at
FROM notifications
WHERE instance_id IS NOT NULL
  AND type IN ('needs_interaction', 'failed', 'completed');

DROP TABLE notifications;
ALTER TABLE notifications_old RENAME TO notifications;

CREATE INDEX IF NOT EXISTS idx_notifications_active
    ON notifications (read, dismissed, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_instance
    ON notifications (instance_id);
