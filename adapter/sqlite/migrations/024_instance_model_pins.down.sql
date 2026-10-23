-- SQLite cannot DROP COLUMN portably on the older versions some deployments
-- run, and a table rebuild is out of scope for a down step. Forward-only column
-- removal is the project convention — see migrations/README.md. The columns are
-- inert to code that predates them.
SELECT 1;
