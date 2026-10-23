-- Retain the highest revision of a removed alias across every delete path.
-- A recreated profile is a new incarnation: old CAS requests must never match,
-- and its revision must supersede any pending publication of the old profile.
CREATE TABLE profile_revision_floor (
    alias TEXT PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision >= 1)
);

-- Preserve revisions still knowable on upgrade, including deleted aliases
-- whose committed advancements have not yet been published.
INSERT INTO profile_revision_floor (alias, revision)
SELECT alias, MAX(revision) FROM (
    SELECT alias, revision FROM profiles
    UNION ALL
    SELECT alias, revision FROM profile_advancement_journal
) GROUP BY alias;

CREATE TRIGGER profiles_revision_floor_on_delete
AFTER DELETE ON profiles
BEGIN
    INSERT INTO profile_revision_floor (alias, revision)
    VALUES (OLD.alias, OLD.revision)
    ON CONFLICT(alias) DO UPDATE SET revision = MAX(revision, excluded.revision);
END;
