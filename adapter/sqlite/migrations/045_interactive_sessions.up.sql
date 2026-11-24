-- Remote Runners Phase 4: durable interactive sessions.
-- Times in these tables are unix milliseconds (INTEGER) so lease expiry and
-- intent ordering compare exactly (timeToString is second-granular).

-- One row per session lease key ("run:<id>" or "resolution:<run>/<merge>/<attempt>").
-- token NULL = released. generation is the session generation: it only grows.
CREATE TABLE turn_leases (
    key        TEXT PRIMARY KEY,
    token      TEXT,
    generation INTEGER NOT NULL DEFAULT 1,
    expires_at INTEGER NOT NULL DEFAULT 0,
    holder     TEXT NOT NULL DEFAULT '',
    boot_id    TEXT NOT NULL DEFAULT ''
);

-- An accepted interactive operation, persisted before dispatch/ack and
-- completed only with the turn commit (RR-12).
CREATE TABLE turn_intents (
    id                   TEXT PRIMARY KEY,
    request_id           TEXT NOT NULL UNIQUE,
    accepted_binding     TEXT NOT NULL DEFAULT '{}',
    lease_key            TEXT NOT NULL,
    run_id               TEXT NOT NULL,
    invocation_id        TEXT NOT NULL,
    node_id              TEXT NOT NULL,
    turn_index           INTEGER NOT NULL,
    kind                 TEXT NOT NULL CHECK (kind IN ('first', 'message', 'end', 'abandon', 'move')),
    prompt               TEXT NOT NULL DEFAULT '',
    system_prompt        TEXT NOT NULL DEFAULT '',
    nonce                TEXT NOT NULL DEFAULT '',
    message              TEXT NOT NULL DEFAULT '',
    model                TEXT NOT NULL DEFAULT '',
    tools                TEXT NOT NULL DEFAULT '[]',
    prior_checkpoint_ref TEXT NOT NULL DEFAULT '',
    session_generation   INTEGER NOT NULL DEFAULT 0,
    operation_id         TEXT NOT NULL DEFAULT '',
    created_at           INTEGER NOT NULL,
    completed_at         INTEGER
);
CREATE INDEX idx_turn_intents_pending ON turn_intents (completed_at, lease_key, created_at);

-- Committed session moves (never for load).
CREATE TABLE session_moves (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    node_id       TEXT NOT NULL,
    from_runner   TEXT NOT NULL,
    to_runner     TEXT NOT NULL,
    reason        TEXT NOT NULL CHECK (reason IN ('exhausted', 'logged_out', 'drain', 'runner_lost')),
    lossy         INTEGER NOT NULL DEFAULT 0,
    notice        TEXT NOT NULL DEFAULT '',
    generation    INTEGER NOT NULL,
    at            INTEGER NOT NULL
);
CREATE INDEX idx_session_moves_run ON session_moves (run_id, node_id, at);

ALTER TABLE node_executions ADD COLUMN session_invocation_id TEXT;
ALTER TABLE node_executions ADD COLUMN session_checkpoint_ref TEXT;
ALTER TABLE node_executions ADD COLUMN session_pin_runner TEXT;
ALTER TABLE node_executions ADD COLUMN session_generation INTEGER NOT NULL DEFAULT 0;

-- Private conflict-resolution staging resources; source_dir stays server-local.
CREATE TABLE resolution_resources (
    id               TEXT PRIMARY KEY,
    group_run_id     TEXT NOT NULL,
    merge_index      INTEGER NOT NULL CHECK (merge_index >= 0),
    attempt          INTEGER NOT NULL CHECK (attempt >= 1),
    task_key         TEXT NOT NULL DEFAULT '',
    source_dir       TEXT NOT NULL,
    work_dir         TEXT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    materialized_ref TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    UNIQUE (group_run_id, merge_index, attempt)
);
