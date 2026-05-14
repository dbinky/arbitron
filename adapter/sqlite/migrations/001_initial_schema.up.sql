-- Consolidated schema for Arbitron (SQLite).
-- Merges Postgres migrations 001–008 into a single SQLite-compatible DDL.

CREATE TABLE IF NOT EXISTS users (
    id         TEXT PRIMARY KEY,
    username   TEXT NOT NULL UNIQUE,
    role       TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT OR IGNORE INTO users (id, username, role)
VALUES ('00000000-0000-0000-0000-000000000001', 'local', 'admin');

CREATE TABLE IF NOT EXISTS instances (
    id                  TEXT PRIMARY KEY,
    owner_id            TEXT NOT NULL REFERENCES users(id),
    workflow_name       TEXT NOT NULL,
    workflow_snapshot    TEXT NOT NULL,
    state               TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'paused')),
    priority            TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('high', 'normal', 'low')),
    variables           TEXT NOT NULL DEFAULT '{}',
    original_variables  TEXT NOT NULL DEFAULT '{}',
    current_node_id     TEXT,
    failed_node_id      TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at        TEXT,
    queued_at           TEXT,
    waiting_until       TEXT,
    webhook_token       TEXT,
    webhook_secret_hash TEXT,
    webhook_received_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_instances_queue_order
    ON instances (state, priority, queued_at) WHERE state = 'queued';
CREATE UNIQUE INDEX IF NOT EXISTS idx_instances_webhook_token
    ON instances (webhook_token) WHERE webhook_token IS NOT NULL;

CREATE TABLE IF NOT EXISTS node_executions (
    id               TEXT PRIMARY KEY,
    instance_id      TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    node_id          TEXT NOT NULL,
    attempt          INTEGER NOT NULL DEFAULT 1,
    status           TEXT NOT NULL CHECK (status IN ('succeeded', 'failed', 'skipped')),
    resolved_inputs  TEXT,
    output           TEXT,
    error            TEXT,
    llm_conversation TEXT,
    llm_model        TEXT,
    llm_tokens_in    INTEGER,
    llm_tokens_out   INTEGER,
    script_stdout    TEXT,
    script_stderr    TEXT,
    script_exit_code INTEGER,
    started_at       TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at     TEXT
);

CREATE INDEX IF NOT EXISTS idx_node_executions_instance_started
    ON node_executions (instance_id, started_at);

CREATE TABLE IF NOT EXISTS variable_snapshots (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    node_id     TEXT NOT NULL,
    variables   TEXT NOT NULL DEFAULT '{}',
    captured_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_variable_snapshots_instance_captured
    ON variable_snapshots (instance_id, captured_at);

CREATE TABLE IF NOT EXISTS notifications (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES instances(id),
    type        TEXT NOT NULL CHECK (type IN ('needs_interaction', 'failed', 'completed')),
    read        INTEGER NOT NULL DEFAULT 0,
    dismissed   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_notifications_active
    ON notifications (read, dismissed, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_instance
    ON notifications (instance_id);
