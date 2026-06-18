-- Workflow identity registry (workflow-management Phase 1). The UNIQUE(name)
-- constraint is the authoritative global-uniqueness rule; each row is the
-- name<->id resolver. The filesystem definition store stays name-keyed and holds
-- the definition body; this table holds identity + uniqueness only.
--
-- owner_id is nullable: a shared/published entry has no single private owner.
--
-- REFERENTIAL INTEGRITY (load-bearing — see migration 017): this table is the
-- IDENTITY AUTHORITY. The workflow_id columns added to instances/schedules/
-- published_workflows in 017 are app-maintained, NULLABLE label references; they
-- are deliberately NOT declared as SQL FOREIGN KEYs to workflow_identity(id).
-- A real FK with ON DELETE SET NULL would NULL a LIVE instance's workflow_id
-- behind the delete-while-running guard's back (the guard keys on workflow_id),
-- which is a worse failure than the transient orphan it would "fix"; and toggling
-- the per-connection foreign_keys PRAGMA for only these columns is an inconsistent
-- landmine for future migrations. Instead, an orphaned reference (workflow_id
-- pointing at a deleted identity) reads back as NULL and is an ACCEPTED TRANSIENT
-- state, reconciled at startup (BackfillReferences / boot-sweep), not a bug.

CREATE TABLE IF NOT EXISTS workflow_identity (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    scope      TEXT NOT NULL,           -- 'private' | 'shared'
    owner_id   TEXT,                    -- nullable for shared
    archived   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workflow_identity_owner ON workflow_identity(owner_id);
