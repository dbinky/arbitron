-- Personal API tokens (Phase 6, Phase 2 §1). The DB stores ONLY the sha256 of
-- the full token (token_hash) plus a clear, non-secret prefix for display. The
-- plaintext arb_<prefix>_<secret> is shown once at creation and is never
-- recoverable. Revocation = row delete.
CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label        TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    last_used_at TEXT
);

-- ListByUser is owner-scoped and ordered newest-first; index the access path.
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens (user_id, created_at DESC);
