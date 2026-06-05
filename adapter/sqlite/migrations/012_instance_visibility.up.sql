-- Carry a workflow's visibility onto the instances it produces (RBAC Phase 3).
-- department: the department the run was stamped under at trigger time
--   (a snapshot; NULL => default group / private). visibility: the run's
--   audience, mirroring the workflow visibility model.
ALTER TABLE instances ADD COLUMN department TEXT;  -- NULL => default group / private
ALTER TABLE instances ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('private','department','public'));

-- List-authorization + Phase-4 dashboard index. Ordered so the disjunctive
-- visibility filter (department IN (...) AND visibility='department', plus the
-- public scan) is a single index-backed range; created_at trails for the
-- ORDER BY created_at DESC paging. Portable to Postgres.
CREATE INDEX IF NOT EXISTS idx_instances_dept_vis
    ON instances (department, visibility, state, created_at);
