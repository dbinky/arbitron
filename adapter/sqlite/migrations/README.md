# SQLite migrations

Numbered `NNN_name.up.sql` / `NNN_name.down.sql` pairs, applied in order.

## Down migrations are forward-only for column additions (one-way)

A `down` migration that follows an `ADD COLUMN` up migration does **not** drop the
column it added (e.g. `003_auth`, `009`–`012` for the RBAC visibility/department
columns and CHECK constraints). Older SQLite versions used by some deployments
cannot `DROP COLUMN` portably, and a table rebuild is out of scope for a down
step, so those columns/constraints are intentionally left in place on rollback.

Consequence: running **down then up again on the same database** will hit a
`duplicate column name` error, because the column the up step adds was never
removed by the down step. This is expected. For iterative local development that
needs a clean re-apply, recreate the database from scratch rather than
down-then-up. The up path remains the only fully supported direction.
