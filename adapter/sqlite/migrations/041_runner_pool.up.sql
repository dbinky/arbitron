-- Remote Runners pool (Phase 1). Runners, their capacity sources with health,
-- the job ledger (intent -> offered -> running -> terminal), attempt history,
-- workspace homes (populated from Phase 3) and the bounded pool event log.

CREATE TABLE IF NOT EXISTS runners (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    built_in         INTEGER NOT NULL DEFAULT 0,
    step_limit       INTEGER NOT NULL DEFAULT 8 CHECK (step_limit BETWEEN 1 AND 256),
    labels           TEXT NOT NULL DEFAULT '{}',
    cordoned         INTEGER NOT NULL DEFAULT 0,
    draining         INTEGER NOT NULL DEFAULT 0,
    compat_state     TEXT NOT NULL DEFAULT 'compatible',
    compat_reason    TEXT NOT NULL DEFAULT '',
    liveness         TEXT NOT NULL DEFAULT 'offline',
    last_contact     TEXT,
    os               TEXT NOT NULL DEFAULT '',
    runner_version   TEXT NOT NULL DEFAULT '',
    harness_versions TEXT NOT NULL DEFAULT '{}',
    workspace_root   TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runner_sources (
    id              TEXT PRIMARY KEY,
    runner_id       TEXT NOT NULL REFERENCES runners(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    credential_ref  TEXT NOT NULL DEFAULT '',
    unavailable_reason TEXT NOT NULL DEFAULT '',
    families        TEXT NOT NULL DEFAULT '[]',
    backends        TEXT NOT NULL DEFAULT '[]',
    models          TEXT NOT NULL DEFAULT '[]',
    auth            TEXT NOT NULL DEFAULT 'unknown',
    capacity        TEXT NOT NULL DEFAULT 'available',
    reset_at        TEXT,
    headroom        REAL,
    windows         TEXT NOT NULL DEFAULT '[]',
    congested       INTEGER NOT NULL DEFAULT 0,
    congested_until TEXT,
    last_probe_at   TEXT,
    last_outcome    TEXT NOT NULL DEFAULT '',
    detail          TEXT NOT NULL DEFAULT '',
    confirmation_model TEXT NOT NULL DEFAULT '',
    revision        INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    updated_at      TEXT NOT NULL,
    UNIQUE (runner_id, kind, credential_ref)
);
CREATE INDEX IF NOT EXISTS idx_runner_sources_runner ON runner_sources (runner_id);

CREATE TABLE IF NOT EXISTS runner_jobs (
    id             TEXT PRIMARY KEY,
    run_id         TEXT NOT NULL,
    invocation_id  TEXT NOT NULL,
    operation_id   TEXT NOT NULL,
    attempt        INTEGER NOT NULL CHECK (attempt >= 1),
    node_id        TEXT NOT NULL,
    op             TEXT NOT NULL,
    group_kind     TEXT NOT NULL,
    group_ref      TEXT NOT NULL DEFAULT '',
    group_family   TEXT NOT NULL DEFAULT '',
    model          TEXT NOT NULL DEFAULT '',
    requirements   TEXT NOT NULL DEFAULT '{}',
    runner_id      TEXT,
    source_id      TEXT,
    generation     INTEGER NOT NULL DEFAULT 0,
    state          TEXT NOT NULL CHECK (state IN ('queued', 'offered', 'running', 'completed', 'failed', 'fenced')),
    lease_until    TEXT,
    reason         TEXT NOT NULL DEFAULT '',
    wait           TEXT,
    result_ref     TEXT NOT NULL DEFAULT '',
    checkpoint_ref TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE (run_id, invocation_id, operation_id, attempt)
);
CREATE INDEX IF NOT EXISTS idx_runner_jobs_runner_state ON runner_jobs (runner_id, state);
CREATE INDEX IF NOT EXISTS idx_runner_jobs_source_state ON runner_jobs (source_id, state);
CREATE INDEX IF NOT EXISTS idx_runner_jobs_state ON runner_jobs (state);
CREATE INDEX IF NOT EXISTS idx_runner_jobs_run ON runner_jobs (run_id);

CREATE TABLE IF NOT EXISTS runner_job_attempts (
    job_id     TEXT NOT NULL REFERENCES runner_jobs(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    runner_id  TEXT NOT NULL,
    source_id  TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL DEFAULT '',
    outcome    TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    ended_at   TEXT,
    PRIMARY KEY (job_id, generation)
);

CREATE TABLE IF NOT EXISTS workspace_homes (
    run_id             TEXT PRIMARY KEY,
    runner_id          TEXT NOT NULL DEFAULT '',
    generation         INTEGER NOT NULL DEFAULT 0,
    checkpoint_version INTEGER NOT NULL DEFAULT 0,
    dirty              INTEGER NOT NULL DEFAULT 0,
    updated_at         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pool_events (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT NOT NULL UNIQUE,
    at            TEXT NOT NULL,
    kind          TEXT NOT NULL,
    runner_id     TEXT NOT NULL DEFAULT '',
    source_id     TEXT NOT NULL DEFAULT '',
    run_id        TEXT,
    cause_kind    TEXT NOT NULL DEFAULT '',
    cause_actor   TEXT NOT NULL DEFAULT '',
    cause_outcome TEXT NOT NULL DEFAULT '',
    detail        TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_pool_events_run ON pool_events (run_id);
