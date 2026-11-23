-- A session may accept only one unresolved operation. Existing overlapping
-- intents require operator resolution; migration must not silently discard them.
CREATE UNIQUE INDEX idx_turn_intents_unique_pending ON turn_intents (lease_key) WHERE completed_at IS NULL;
