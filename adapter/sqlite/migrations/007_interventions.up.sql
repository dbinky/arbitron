-- instance_interventions: audit trail of actions taken on an instance by an
-- actor who is NOT the instance owner (admin intervention). Owner self-actions
-- are intentionally not recorded. Visible to the owner and to admins.
CREATE TABLE IF NOT EXISTS instance_interventions (
    id            TEXT PRIMARY KEY,
    instance_id   TEXT NOT NULL,
    actor_user_id TEXT NOT NULL,
    action        TEXT NOT NULL,            -- pause|resume|cancel|set_priority|retry|rerun|interactive.message|interactive.ended
    detail        TEXT,                     -- nullable; e.g. new priority value, end reason
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    FOREIGN KEY (instance_id) REFERENCES instances(id)
);

CREATE INDEX IF NOT EXISTS idx_instance_interventions_instance
    ON instance_interventions (instance_id, created_at);
