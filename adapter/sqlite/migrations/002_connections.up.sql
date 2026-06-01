-- Connection store: endpoint metadata plus the AES-GCM-sealed API key.
-- The plaintext key is never stored: only api_key_cipher / api_key_nonce
-- (the Seal output) live here. extras holds the map[string]string as a JSON
-- object (the SQLite analogue of the design's extras jsonb).

CREATE TABLE IF NOT EXISTS connections (
    name           TEXT PRIMARY KEY,
    provider       TEXT NOT NULL,
    base_url       TEXT NOT NULL,
    extras         TEXT NOT NULL DEFAULT '{}',
    api_key_cipher BLOB NOT NULL,
    api_key_nonce  BLOB NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);
