-- Reverse of 003_auth.up.sql. migrate.go only runs *.up.sql; this file documents
-- the rollback (SQLite cannot DROP COLUMN portably, so subject/email are left).
DROP INDEX IF EXISTS idx_sessions_user;
DROP INDEX IF EXISTS idx_sessions_expires_at;
DROP TABLE IF EXISTS sessions;
DROP INDEX IF EXISTS idx_users_subject;
