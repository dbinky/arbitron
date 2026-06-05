-- Visibility metadata for the shared workspace. The shared workflow content
-- remains a single copy under shared/<name>; only this row's level changes.
-- The DEFAULT 'public' IS the data migration (spec §Migration of Existing
-- Data): every pre-existing shared row becomes public, preserving today's
-- "everyone can see it" behavior. A 'department' row must carry a non-NULL
-- department (enforced in the domain service; SQLite cannot CHECK across the
-- DEFAULT cleanly so the guard lives in code).
ALTER TABLE published_workflows
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'
        CHECK (visibility IN ('department','public'));

ALTER TABLE published_workflows
    ADD COLUMN department TEXT;  -- NULL for public; set for department

CREATE INDEX IF NOT EXISTS idx_published_visibility
    ON published_workflows(visibility, department);
