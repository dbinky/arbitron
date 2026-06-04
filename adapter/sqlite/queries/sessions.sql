-- name: CreateSession :exec
INSERT INTO sessions (id_hash, user_id, created_at, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetSession :one
-- Returns the row ONLY if not yet expired; an expired row is treated as absent
-- so the adapter maps both "no row" and "expired" to ErrNotFound uniformly.
SELECT id_hash, user_id, created_at, expires_at
FROM sessions
WHERE id_hash = ? AND expires_at > ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= ?;
