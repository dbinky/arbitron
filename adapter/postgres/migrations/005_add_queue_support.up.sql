-- Add 'waiting' to instance_state enum
ALTER TYPE instance_state ADD VALUE IF NOT EXISTS 'waiting';

-- Add queued_at column
ALTER TABLE instances ADD COLUMN IF NOT EXISTS queued_at TIMESTAMPTZ;

-- Index for efficient queue ordering (state + priority + queued_at)
CREATE INDEX IF NOT EXISTS idx_instances_queue_order ON instances (state, priority, queued_at)
    WHERE state = 'queued';
