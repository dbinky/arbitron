CREATE TABLE profile_seed_state (
    id INTEGER PRIMARY KEY CHECK (id = 1)
);
CREATE TABLE profile_seed_pending (
    alias TEXT PRIMARY KEY NOT NULL,
    position INTEGER NOT NULL,
    profile_json TEXT NOT NULL
);
-- Existing installations have already ceded config ownership to the DB.
INSERT INTO profile_seed_state (id) SELECT 1 WHERE EXISTS (SELECT 1 FROM profiles);
