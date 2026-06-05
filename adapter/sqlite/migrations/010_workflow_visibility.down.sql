DROP INDEX IF EXISTS idx_published_visibility;
ALTER TABLE published_workflows DROP COLUMN department;
ALTER TABLE published_workflows DROP COLUMN visibility;
