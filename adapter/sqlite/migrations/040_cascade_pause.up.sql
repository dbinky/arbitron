-- Cascade-pause marker. When pausing a run cascades to its spawned
-- descendants, each descendant records WHICH ancestor's pause stopped it and
-- the state it was in, so resuming that ancestor restores only what its pause
-- stopped (a pause an operator placed on a child is left alone) and returns a
-- previously-queued child to the queue instead of running it out of turn.
-- No foreign key, for the same reason parent_instance_id has none.
ALTER TABLE instances ADD COLUMN paused_by_instance_id TEXT;
ALTER TABLE instances ADD COLUMN paused_from_state TEXT;
