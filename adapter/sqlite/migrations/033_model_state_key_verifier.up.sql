-- G6: the key file carries only a length check, so a corrupted or substituted
-- same-length model-state key would boot and silently invalidate encrypted
-- attempts and path fingerprints. Store a keyed verifier (an HMAC, never the
-- key) and refuse any other key. Kept apart from model_state_meta so a 023
-- down/up, which resets the initialization flag, still verifies the key file.
CREATE TABLE model_state_key_verifier (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    verifier TEXT NOT NULL CHECK (length(verifier) = 64)
);
