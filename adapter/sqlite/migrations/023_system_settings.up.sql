CREATE TABLE IF NOT EXISTS system_settings (
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO system_settings (key, value) VALUES
    ('model_catalog.url', 'https://models.dev/api.json'),
    ('model_announcements.teams_webhook_url', '');
