ALTER TABLE published_workflows ADD COLUMN saved_revision TEXT;

-- Layout history deliberately outlives filesystem and registry identities.
CREATE TABLE layout_revisions (
    revision TEXT PRIMARY KEY NOT NULL,
    workflow_id TEXT,
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('private','shared')),
    owner_id TEXT NOT NULL CHECK (scope_kind != 'shared' OR owner_id = ''),
    workflow_name TEXT NOT NULL,
    reason TEXT NOT NULL,
    input_manifest BLOB NOT NULL,
    retained TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','in_progress','passed','needs_attention','failed','superseded')),
    stage TEXT NOT NULL DEFAULT 'queued',
    job_generation INTEGER NOT NULL DEFAULT 1 CHECK (job_generation > 0),
    attempts_used INTEGER NOT NULL DEFAULT 0 CHECK (attempts_used >= 0),
    attempt_limit INTEGER NOT NULL CHECK (attempt_limit >= 1),
    verdict TEXT NOT NULL DEFAULT '',
    findings TEXT NOT NULL DEFAULT '[]',
    unreviewed INTEGER NOT NULL DEFAULT 1 CHECK (unreviewed IN (0,1)),
    failure_stage TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    result_generation INTEGER NOT NULL DEFAULT 0 CHECK (result_generation >= 0),
    cache_generation INTEGER NOT NULL DEFAULT 0 CHECK (cache_generation >= 0),
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at INTEGER,
    requested_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    started_at INTEGER,
    finished_at INTEGER
);
CREATE INDEX idx_layout_destination ON layout_revisions(scope_kind,owner_id,workflow_name);
CREATE INDEX idx_layout_claimable ON layout_revisions(state,lease_expires_at,requested_at,revision);
CREATE TABLE layout_results (
    revision TEXT NOT NULL REFERENCES layout_revisions(revision),
    generation INTEGER NOT NULL CHECK (generation > 0),
    geometry TEXT NOT NULL,
    review TEXT NOT NULL,
    preflight TEXT NOT NULL,
    score TEXT NOT NULL,
    verdict TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (revision,generation)
);
