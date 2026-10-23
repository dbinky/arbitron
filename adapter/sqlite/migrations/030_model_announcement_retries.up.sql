-- 022 is already applied on existing installations. Preserve its immutable events.
ALTER TABLE model_announcement_events ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0);
ALTER TABLE model_announcement_events ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT '';
ALTER TABLE model_announcement_events ADD COLUMN last_code TEXT NOT NULL DEFAULT '';
ALTER TABLE model_announcement_events ADD COLUMN parts TEXT NOT NULL DEFAULT '[]';
ALTER TABLE model_announcement_events ADD COLUMN part_count INTEGER NOT NULL DEFAULT 0 CHECK (part_count >= 0);
ALTER TABLE model_announcement_events ADD COLUMN confirmed_parts TEXT NOT NULL DEFAULT '[]';
CREATE INDEX model_announcement_due ON model_announcement_events(state, next_attempt_at);
