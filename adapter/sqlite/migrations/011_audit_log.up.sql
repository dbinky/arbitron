-- audit_events: append-only record of consequential mutations (workflow
-- publish/visibility changes, admin overrides, and — in Phase 3 — lead
-- interventions). Reads are never audited (spec §Audit). Deliberately small.
CREATE TABLE IF NOT EXISTS audit_events (
    id          TEXT PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    actor_id    TEXT NOT NULL,
    action      TEXT NOT NULL,   -- workflow.publish | workflow.visibility_change | admin.override
    target      TEXT NOT NULL,   -- workflow name / instance id
    detail      TEXT NOT NULL DEFAULT '{}'  -- JSON: {from,to,department,...}
);

CREATE INDEX IF NOT EXISTS idx_audit_occurred ON audit_events(occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_events(actor_id);
