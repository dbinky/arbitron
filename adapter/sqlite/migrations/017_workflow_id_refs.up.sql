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
ALTER TABLE instances           ADD COLUMN workflow_id TEXT;  -- NULL until backfilled
ALTER TABLE schedules           ADD COLUMN workflow_id TEXT;  -- NULL until backfilled
ALTER TABLE published_workflows ADD COLUMN workflow_id TEXT;  -- NULL until backfilled

CREATE INDEX IF NOT EXISTS idx_instances_workflow_id ON instances(workflow_id);
CREATE INDEX IF NOT EXISTS idx_schedules_workflow_id ON schedules(workflow_id);
