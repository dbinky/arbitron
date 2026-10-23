-- name: CreateProfile :exec
INSERT INTO profiles (alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort, selection_mode, follow_provider, follow_tier, created_at, updated_at, revision)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE((SELECT revision FROM profile_revision_floor WHERE profile_revision_floor.alias = ?), 0) + 1);

-- name: UpdateProfile :execrows
UPDATE profiles
SET kind = ?, connection_ref = ?, model = ?, backend = ?, allowed_tools = ?, context_window = ?, effort = ?, selection_mode = ?, follow_provider = ?, follow_tier = ?, revision = revision + 1, updated_at = ?
WHERE alias = ?;

-- name: DeleteProfile :execrows
DELETE FROM profiles WHERE alias = ?;

-- name: GetProfile :one
SELECT alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort, selection_mode, follow_provider, follow_tier, revision
FROM profiles WHERE alias = ?;

-- name: ListProfiles :many
SELECT alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort, selection_mode, follow_provider, follow_tier, revision
FROM profiles ORDER BY alias;

-- name: CountProfiles :one
SELECT COUNT(*) FROM profiles;
