DROP INDEX IF EXISTS idx_instances_queue_order;
ALTER TABLE instances DROP COLUMN IF EXISTS queued_at;
-- Note: cannot remove enum value in PostgreSQL, leave 'waiting' in place
