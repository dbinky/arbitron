-- Catalog state is independent of the already-applied execution revisions
-- migration (023). In particular, never recreate/reset model_state_meta.
CREATE TABLE model_catalog_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    fetched_at TEXT NOT NULL,
    unmatched_targets TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE model_catalog_slices (
    provider TEXT PRIMARY KEY,
    slice_json TEXT NOT NULL
);
-- Historical source inputs survive removal from the current catalog.
CREATE TABLE model_catalog_models (
    provider TEXT NOT NULL,
    native_id TEXT NOT NULL,
    canonical_model_id TEXT NOT NULL,
    release_date TEXT NOT NULL,
    source_ref TEXT NOT NULL,
    policy_revision INTEGER NOT NULL,
    entry_json TEXT NOT NULL,
    PRIMARY KEY (provider, native_id)
);
CREATE TABLE model_usability_evidence (
    canonical_id TEXT NOT NULL,
    path_key TEXT NOT NULL,
    config_revision TEXT NOT NULL,
    native_model TEXT NOT NULL,
    checked_at TEXT NOT NULL,
    proof_ref TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('confirmed', 'unavailable', 'unknown')),
    PRIMARY KEY (canonical_id, path_key, config_revision)
);
CREATE TABLE model_first_observations (
    canonical_id TEXT PRIMARY KEY,
    observation TEXT NOT NULL
);
CREATE TABLE model_refresh_reports (
    id TEXT PRIMARY KEY,
    report TEXT NOT NULL,
    publication_pending INTEGER NOT NULL CHECK (publication_pending IN (0, 1))
);
CREATE TABLE model_refresh_advancements (
    report_id TEXT NOT NULL REFERENCES model_refresh_reports(id),
    advancement_id TEXT NOT NULL REFERENCES profile_advancement_journal(id),
    PRIMARY KEY (report_id, advancement_id)
);
CREATE TABLE model_publication_acks (
    advancement_id TEXT PRIMARY KEY REFERENCES profile_advancement_journal(id)
);
CREATE TABLE model_announcement_events (
    id TEXT PRIMARY KEY,
    canonical_id TEXT NOT NULL UNIQUE REFERENCES model_first_observations(canonical_id),
    payload TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('awaiting_setting', 'pending', 'in_flight', 'delivery_unknown', 'announced', 'suppressed')),
    claim_id TEXT NOT NULL DEFAULT '',
    lease_until TEXT NOT NULL DEFAULT ''
);
CREATE TABLE model_probe_cursors (
    path_config TEXT PRIMARY KEY,
    cursor TEXT NOT NULL
);
