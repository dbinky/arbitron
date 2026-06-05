DROP INDEX IF EXISTS idx_instances_dept_vis;
-- SQLite cannot DROP COLUMN on older versions used by some deployments; rebuild
-- is out of scope for a down migration. Recreating the table is intentionally
-- omitted — forward-only is the project convention (see prior down files).
