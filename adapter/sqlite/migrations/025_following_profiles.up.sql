ALTER TABLE profiles ADD COLUMN selection_mode TEXT NOT NULL DEFAULT 'pinned'
    CHECK (selection_mode IN ('pinned', 'following'));
ALTER TABLE profiles ADD COLUMN follow_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE profiles ADD COLUMN follow_tier TEXT NOT NULL DEFAULT '';
ALTER TABLE profiles ADD COLUMN revision INTEGER NOT NULL DEFAULT 1
    CHECK (revision >= 1 AND (
        (selection_mode = 'pinned' AND follow_provider = '' AND follow_tier = '') OR
        (selection_mode = 'following' AND follow_provider <> '' AND follow_tier <> '')
    ));

CREATE TABLE profile_advancement_journal (
    id TEXT PRIMARY KEY NOT NULL,
    refresh_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    from_model TEXT NOT NULL,
    to_model TEXT NOT NULL,
    canonical_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    at TEXT NOT NULL
);
