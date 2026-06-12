-- Profile registry: the alias -> backend selection that node kinds resolve
-- through (profile-editing Phase 2). Profiles carry NO secrets, so there is no
-- cipher column (the endpoint key lives on the referenced connection). The two
-- new axes (context_window, effort) default to 'default' (emit nothing).
--
-- No FK to connections: a profile may be authored before its connection exists;
-- reference integrity is checked by the existing ConnectionChecker at
-- workflow-validation time, not by the schema.

CREATE TABLE IF NOT EXISTS profiles (
    alias          TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,                 -- chat | agent
    connection_ref TEXT NOT NULL DEFAULT '',      -- '' = claude-cli no-key
    model          TEXT NOT NULL DEFAULT '',
    backend        TEXT NOT NULL DEFAULT '',
    allowed_tools  TEXT NOT NULL DEFAULT '[]',    -- JSON array
    context_window TEXT NOT NULL DEFAULT 'default',
    effort         TEXT NOT NULL DEFAULT 'default',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
