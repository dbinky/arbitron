-- Phase 6 (Identity Core): add the IdP-stable subject and email to users and
-- create the server-side sessions table. The default 'local' user keeps
-- role='admin' from migration 001; org-provisioned users carry a non-null subject.

ALTER TABLE users ADD COLUMN subject TEXT;
-- email is nullable (the pre-existing 'local' user has none). It is the persisted
-- authority for the bootstrap-admin rule consumed by Phase 2 (provisioning AND the
-- demotion floor), which matches AuthConfig.BootstrapAdmins on email (case-insensitive),
-- falling back to subject. Stored as provided by the IdP claim; comparisons lower-case.
ALTER TABLE users ADD COLUMN email TEXT;
-- Unique only among non-null subjects: the pre-existing 'local' user has a NULL
-- subject and must not collide with itself or block other NULLs.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_subject
    ON users (subject) WHERE subject IS NOT NULL;

-- Sessions: id_hash is sha256(raw cookie token) -- the raw token is NEVER stored
-- (the cookie holds it). A DB read leak therefore cannot mint a session.
CREATE TABLE IF NOT EXISTS sessions (
    id_hash    TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
