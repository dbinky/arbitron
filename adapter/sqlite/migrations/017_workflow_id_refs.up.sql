-- Workflow-management Phase 2 Task 1: add a nullable workflow_id reference to the
-- three tables that today key a workflow purely by name. workflow_name is RETAINED
-- as a human-facing label; workflow_id (when present) is the stable identity from
-- the Phase-1 workflow_identity registry. A one-time BackfillReferences resolves
-- each existing row's name -> id; rows whose name no longer resolves keep a NULL
-- workflow_id (a dangling historical label) — this is additive and changes no
-- read/trigger behavior yet.
--
-- published_workflows keys the workflow by its `name` column (its PRIMARY KEY), so
-- it gains workflow_id the same way.
--
-- REFERENTIAL INTEGRITY (load-bearing): these workflow_id columns are
-- APP-MAINTAINED, NULLABLE LABELS — they are intentionally NOT SQL FOREIGN KEYs to
-- workflow_identity(id). The workflow_identity registry (migration 016) is the
-- identity authority. Three reasons the FK is omitted rather than just unwritten:
--   1. ON DELETE SET NULL would NULL a LIVE instance's workflow_id behind the
--      delete-while-running guard's back (the guard counts active runs by
--      workflow_id), defeating the very invariant this column exists to support.
--   2. ON DELETE RESTRICT would block legitimate identity cleanup behind historical
--      rows that only need the label for display/rerun-fallback.
--   3. Flipping the per-connection foreign_keys PRAGMA for these three columns only
--      is an inconsistent landmine for every future migration.
-- Therefore an orphan (workflow_id referencing a deleted identity) is an ACCEPTED
-- TRANSIENT state: it reads back as a dangling label, never breaks a running
-- instance, and is reconciled at startup (BackfillReferences and the boot-sweep).
-- A reader must treat a NULL/dangling workflow_id as "label only", not as a bug.
ALTER TABLE instances           ADD COLUMN workflow_id TEXT;  -- NULL until backfilled
ALTER TABLE schedules           ADD COLUMN workflow_id TEXT;  -- NULL until backfilled
ALTER TABLE published_workflows ADD COLUMN workflow_id TEXT;  -- NULL until backfilled

CREATE INDEX IF NOT EXISTS idx_instances_workflow_id ON instances(workflow_id);
CREATE INDEX IF NOT EXISTS idx_schedules_workflow_id ON schedules(workflow_id);
