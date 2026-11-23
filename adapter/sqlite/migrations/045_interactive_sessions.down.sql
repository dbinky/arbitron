-- Column additions on node_executions are forward-only (see migrations/README.md).
DROP INDEX IF EXISTS idx_session_moves_run;
DROP TABLE IF EXISTS session_moves;
DROP INDEX IF EXISTS idx_turn_intents_pending;
DROP TABLE IF EXISTS turn_intents;
DROP TABLE IF EXISTS turn_leases;
