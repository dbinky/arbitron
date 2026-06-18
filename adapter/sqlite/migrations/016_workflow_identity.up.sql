-- Workflow identity registry (workflow-management Phase 1). The UNIQUE(name)
-- constraint is the authoritative global-uniqueness rule; each row is the
-- name<->id resolver. The filesystem definition store stays name-keyed and holds
-- the definition body; this table holds identity + uniqueness only.
--
-- owner_id is nullable: a shared/published entry has no single private owner.

CREATE TABLE IF NOT EXISTS workflow_identity (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    scope      TEXT NOT NULL,           -- 'private' | 'shared'
    owner_id   TEXT,                    -- nullable for shared
    archived   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workflow_identity_owner ON workflow_identity(owner_id);
