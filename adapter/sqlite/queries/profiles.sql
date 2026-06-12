-- name: CreateProfile :exec
INSERT INTO profiles (alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateProfile :execrows
UPDATE profiles
SET kind = ?, connection_ref = ?, model = ?, backend = ?, allowed_tools = ?, context_window = ?, effort = ?, updated_at = ?
WHERE alias = ?;

-- name: DeleteProfile :execrows
DELETE FROM profiles WHERE alias = ?;

-- name: GetProfile :one
SELECT alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort
FROM profiles WHERE alias = ?;

-- name: ListProfiles :many
SELECT alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort
FROM profiles ORDER BY alias;

-- name: CountProfiles :one
SELECT COUNT(*) FROM profiles;
