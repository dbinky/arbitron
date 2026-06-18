DROP INDEX IF EXISTS idx_instances_workflow_id;
DROP INDEX IF EXISTS idx_schedules_workflow_id;
-- SQLite cannot DROP COLUMN on older versions used by some deployments; rebuild
-- is out of scope for a down migration. Recreating the table is intentionally
-- omitted — forward-only column removal is the project convention (see
-- 012_instance_visibility and 014_instance_trigger_origin down migrations).
