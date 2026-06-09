DROP INDEX IF EXISTS idx_instances_schedule_id;
-- SQLite cannot DROP COLUMN on older versions used by some deployments; rebuild
-- is out of scope for a down migration. Recreating the table is intentionally
-- omitted — forward-only is the project convention (see 012_instance_visibility).
