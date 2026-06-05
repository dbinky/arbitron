-- RBAC Phase 1: per-user department membership, snapshotted at login by the
-- provisioner. Departments are derived from normalized group-claim values; there
-- is no separate department registry. Non-interactive triggers (webhooks / API
-- tokens) read this stored set because no live token is present for them.
--
-- Postgres-ready: standard SQL only (composite PK, FK with ON DELETE CASCADE,
-- a single secondary index). No SQLite-only constructs.
CREATE TABLE IF NOT EXISTS user_departments (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    department TEXT NOT NULL,
    PRIMARY KEY (user_id, department)
);

-- The dept index serves later phases' "who is in department X" reads (dashboard,
-- department-visibility filters). Added now so the schema is forward-compatible.
CREATE INDEX IF NOT EXISTS idx_user_departments_dept ON user_departments(department);
