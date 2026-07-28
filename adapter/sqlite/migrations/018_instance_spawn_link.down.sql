DROP INDEX IF EXISTS idx_instances_parent;
-- SQLite cannot DROP COLUMN portably on the older versions some deployments
-- run, and a table rebuild is out of scope for a down step. Forward-only column
-- removal is the project convention — see the 012 / 014 / 017 down migrations
-- and migrations/README.md.
