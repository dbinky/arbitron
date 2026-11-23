-- Remote runners Phase 3: workspace mobility, durable step commits, transfers,
-- drain and runner-held copy inventory. Run IDs are not foreign keys: these rows
-- describe runner-side state that can outlive instance cleanup ordering, and
-- every owner task deletes its own rows.

-- Immutable artifact candidates (task 2); only committed home pointers select progress.
-- Bytes are durable before prepared-row insertion; validated transaction commits progress.
CREATE TABLE workspace_checkpoints (
    run_id         TEXT    NOT NULL,
    version        INTEGER NOT NULL CHECK (version >= 1),
    manifest_hash  TEXT    NOT NULL CHECK (length(manifest_hash) = 64 AND manifest_hash NOT GLOB '*[^0-9a-fA-F]*'),
    archive_path   TEXT    NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    job_id         TEXT,
    job_generation INTEGER CHECK(job_generation >= 1),
    state TEXT NOT NULL CHECK (state IN ('prepared','committed')),
    base_ref TEXT NOT NULL DEFAULT '',
    resource_key TEXT NOT NULL,
    resource_owner TEXT NOT NULL,
    resource_generation INTEGER NOT NULL CHECK(resource_generation >= 1),
    resource_token TEXT NOT NULL CHECK (
        length(resource_token) = 36
        AND substr(resource_token,9,1) = '-' AND substr(resource_token,14,1) = '-'
        AND substr(resource_token,19,1) = '-' AND substr(resource_token,24,1) = '-'
        AND length(replace(resource_token,'-','')) = 32
        AND replace(resource_token,'-','') NOT GLOB '*[^0-9a-fA-F]*'
        AND replace(resource_token,'-','') <> '00000000000000000000000000000000'
    ),
    created_at     TEXT    NOT NULL,
    CHECK(state != 'prepared' OR (job_id IS NOT NULL AND job_id != '' AND job_generation IS NOT NULL)),
    PRIMARY KEY (run_id, version)
);

-- Home handoff history (task 3). One row per home generation.
CREATE TABLE home_moves (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT    NOT NULL,
    from_runner TEXT    NOT NULL DEFAULT '',
    to_runner   TEXT    NOT NULL DEFAULT '',
    reason      TEXT    NOT NULL,
    bytes       INTEGER NOT NULL DEFAULT 0 CHECK (bytes >= 0),
    generation  INTEGER NOT NULL CHECK (generation >= 1),
    target_job_id TEXT, invocation_id TEXT, node_id TEXT, operation_id TEXT,
    from_generation INTEGER, to_generation INTEGER,
    moved_at    TEXT    NOT NULL,
    UNIQUE (run_id, generation)
);
CREATE INDEX idx_home_moves_run ON home_moves (run_id, generation);

-- Short-lived transfer tokens (task 7). Only the SHA-256 of the token is stored.
CREATE TABLE transfer_sessions (
    token_hash     TEXT    PRIMARY KEY CHECK (length(token_hash) = 64),
    job_id         TEXT,
    generation     INTEGER,
    maintenance_operation_id TEXT,
    base_ref TEXT NOT NULL DEFAULT '',
    bundle_hash TEXT NOT NULL DEFAULT '',
    resource_key TEXT NOT NULL, resource_owner TEXT NOT NULL,
    resource_generation INTEGER NOT NULL CHECK(resource_generation >= 1),
    resource_token TEXT NOT NULL CHECK (
        length(resource_token) = 36
        AND substr(resource_token,9,1) = '-' AND substr(resource_token,14,1) = '-'
        AND substr(resource_token,19,1) = '-' AND substr(resource_token,24,1) = '-'
        AND length(replace(resource_token,'-','')) = 32
        AND replace(resource_token,'-','') NOT GLOB '*[^0-9a-fA-F]*'
        AND replace(resource_token,'-','') <> '00000000000000000000000000000000'
    ),
    runner_id      TEXT    NOT NULL,
    run_id         TEXT    NOT NULL,
    direction      TEXT    NOT NULL CHECK (direction IN ('pull', 'push', 'bundle')),
    checkpoint_ref TEXT    NOT NULL DEFAULT '',
    expires_at     TEXT    NOT NULL,
    parts          TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(parts)),
    created_at     TEXT    NOT NULL
);
CREATE INDEX idx_transfer_sessions_expiry ON transfer_sessions (expires_at);
CREATE INDEX idx_transfer_sessions_job ON transfer_sessions (job_id, generation);

-- Durable step commits (task 5, RR-10): result + checkpoint + continuation.
CREATE TABLE step_commits (
    invocation_id  TEXT    PRIMARY KEY,
    run_id         TEXT    NOT NULL,
    node_id        TEXT    NOT NULL,
    execution_id   TEXT    NOT NULL,
    next_node_id   TEXT    NOT NULL DEFAULT '',
    variables      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(variables)),
    committed_at   TEXT    NOT NULL
);
CREATE INDEX idx_step_commits_run ON step_commits (run_id, committed_at);
CREATE TABLE workspace_leases (
    resource_key TEXT PRIMARY KEY,
    holder TEXT NOT NULL,
    owner TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK(generation >= 1),
    token TEXT NOT NULL CHECK (
        length(token) = 36
        AND substr(token,9,1) = '-' AND substr(token,14,1) = '-'
        AND substr(token,19,1) = '-' AND substr(token,24,1) = '-'
        AND length(replace(token,'-','')) = 32
        AND replace(token,'-','') NOT GLOB '*[^0-9a-fA-F]*'
        AND replace(token,'-','') <> '00000000000000000000000000000000'
    ),
    active INTEGER NOT NULL CHECK(active IN (0,1))
);
CREATE TABLE step_commit_operations (
 invocation_id TEXT NOT NULL, job_id TEXT NOT NULL, job_runner_id TEXT NOT NULL,
 job_generation INTEGER NOT NULL CHECK(job_generation>=1), checkpoint_ref TEXT,
 resource_key TEXT, resource_owner TEXT, resource_generation INTEGER, resource_token TEXT,
 PRIMARY KEY(invocation_id,job_id)
);
CREATE TABLE step_suspensions (
 invocation_id TEXT NOT NULL, operation_id TEXT NOT NULL, kind TEXT NOT NULL,
 child_link_id TEXT, state TEXT NOT NULL CHECK(json_valid(state)), committed_at TEXT NOT NULL,
 PRIMARY KEY(invocation_id,operation_id)
);
CREATE TABLE builtin_processes (
 job_id TEXT NOT NULL, generation INTEGER NOT NULL, guardian_pid INTEGER NOT NULL,
 process_start_marker TEXT NOT NULL, state TEXT NOT NULL, PRIMARY KEY(job_id,generation)
);


-- Drain progress items (P3 T12). Blocked items stay visible until resolved.
CREATE TABLE drain_items (
    runner_id  TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('workspace', 'session')),
    run_id     TEXT NOT NULL DEFAULT '',
    group_key  TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL CHECK (state IN ('pending', 'moving', 'blocked', 'done')),
    reason     TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (runner_id, kind, run_id)
);

-- Which runner holds which home generation of a run's workspace (P3 T11).
CREATE TABLE runner_copies (
    run_id          TEXT    NOT NULL,
    runner_id       TEXT    NOT NULL,
    home_generation INTEGER NOT NULL CHECK (home_generation >= 0),
    state           TEXT    NOT NULL CHECK (state IN ('active', 'stale', 'delete_pending', 'deleted')),
    updated_at      TEXT    NOT NULL,
    PRIMARY KEY (run_id, runner_id)
);
CREATE INDEX idx_runner_copies_runner ON runner_copies (runner_id, state);

ALTER TABLE workspace_homes ADD COLUMN non_portable_reason TEXT NOT NULL DEFAULT '';

-- Retry budget and deadline survive process loss independently of the live loop.
CREATE TABLE node_retry_progress (
 invocation_id TEXT PRIMARY KEY REFERENCES node_invocations(id) ON DELETE CASCADE,
 attempt INTEGER NOT NULL CHECK(attempt >= 1),
 retry_not_before TEXT,
 exhausted INTEGER NOT NULL DEFAULT 0 CHECK(exhausted IN (0,1)),
 last_execution_id TEXT REFERENCES node_executions(id)
);
CREATE TABLE node_retry_failures (
 invocation_id TEXT NOT NULL REFERENCES node_invocations(id) ON DELETE CASCADE,
 attempt INTEGER NOT NULL CHECK(attempt >= 1),
 execution_id TEXT NOT NULL REFERENCES node_executions(id),
 payload_hash TEXT NOT NULL,
 PRIMARY KEY(invocation_id,attempt)
);
