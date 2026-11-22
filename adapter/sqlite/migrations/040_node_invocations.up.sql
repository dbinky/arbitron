-- Durable per-visit node invocations (Remote Runners RR-01). One row per node
-- VISIT: a graph back-edge commits the previous visit and begins visit+1; a
-- restart reuses the open (uncommitted) row. The runner job ledger keys every
-- dispatched operation on (run, invocation, operation, attempt).
CREATE TABLE IF NOT EXISTS node_invocations (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    node_id      TEXT NOT NULL,
    visit        INTEGER NOT NULL CHECK (visit >= 1),
    created_at   TEXT NOT NULL,
    committed_at TEXT,
    UNIQUE (run_id, node_id, visit)
);

-- At most one OPEN visit per (run, node): the structural guarantee behind
-- "restart reuses the open invocation".
CREATE UNIQUE INDEX IF NOT EXISTS idx_node_invocations_open
    ON node_invocations (run_id, node_id) WHERE committed_at IS NULL;
