-- Publisher tracking for the shared workspace. `name` is the published name and
-- is unique across the shared scope (enforced as PK). Authority for the
-- unique-name and publisher-only-overwrite rules; the shared/<name> folder is
-- the artifact, this row is the record.
CREATE TABLE IF NOT EXISTS published_workflows (
    name         TEXT PRIMARY KEY,
    publisher_id TEXT NOT NULL REFERENCES users(id),
    source_name  TEXT NOT NULL,
    published_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

CREATE INDEX IF NOT EXISTS idx_published_workflows_publisher
    ON published_workflows(publisher_id);
