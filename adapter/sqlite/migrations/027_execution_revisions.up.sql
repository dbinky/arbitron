ALTER TABLE connections ADD COLUMN config_revision INTEGER NOT NULL DEFAULT 1 CHECK (config_revision >= 1);

CREATE TABLE model_state_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    secret_initialized INTEGER NOT NULL DEFAULT 0 CHECK (secret_initialized IN (0, 1))
);
INSERT INTO model_state_meta (id) VALUES (1);
