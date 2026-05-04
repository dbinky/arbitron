-- name: GetDefaultUser :one
SELECT id, username, role, created_at FROM users WHERE username = 'local' LIMIT 1;
