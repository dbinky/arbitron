-- name: CreateAPIToken :exec
INSERT INTO api_tokens (id, user_id, label, token_hash, prefix)
VALUES (?, ?, ?, ?, ?);

-- name: GetAPITokenByHash :one
SELECT id, user_id, label, token_hash, prefix, created_at, last_used_at
FROM api_tokens WHERE token_hash = ? LIMIT 1;

-- name: ListAPITokensByUser :many
SELECT id, user_id, label, token_hash, prefix, created_at, last_used_at
FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE id = ? AND user_id = ?;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;
