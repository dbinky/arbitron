-- OAuth 2.0 device authorization grant (Phase 6, Phase 2 §2). The polling
-- secret (device_code) is stored ONLY as its sha256 (device_code_hash); the
-- short human user_code is stored in clear (it is low-entropy and shown to the
-- approver). user_id is NULL until an authenticated browser approves the row.
CREATE TABLE IF NOT EXISTS device_codes (
    device_code_hash TEXT PRIMARY KEY,
    user_code        TEXT NOT NULL UNIQUE,
    status           TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','approved','denied','consumed')),
    user_id          TEXT REFERENCES users(id) ON DELETE CASCADE,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    expires_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_device_codes_user_code ON device_codes (user_code);
