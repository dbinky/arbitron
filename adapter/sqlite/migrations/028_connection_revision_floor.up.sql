-- High-water config_revision of every deleted connection name. A recreated name
-- starts above its floor so config_revision never repeats for a name: it is the
-- only credential-change signal (the key itself is not fingerprintable).
CREATE TABLE connection_revision_floor (
    name TEXT PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision >= 1)
);

-- Seeded by trigger so every delete path (not just ConnectionRepo.Delete)
-- records the floor atomically with the row removal.
CREATE TRIGGER connections_revision_floor_on_delete
AFTER DELETE ON connections
BEGIN
    INSERT INTO connection_revision_floor (name, revision)
    VALUES (OLD.name, OLD.config_revision)
    ON CONFLICT(name) DO UPDATE SET revision = MAX(revision, excluded.revision);
END;
