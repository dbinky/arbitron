-- name: CreateConnection :exec
INSERT INTO connections (name, provider, base_url, extras, api_key_cipher, api_key_nonce, config_revision)
VALUES (?, ?, ?, ?, ?, ?, COALESCE((SELECT revision FROM connection_revision_floor WHERE connection_revision_floor.name = ?), 0) + 1);

-- name: UpdateConnectionWithKey :execrows
UPDATE connections
SET provider = ?, base_url = ?, extras = ?, api_key_cipher = ?, api_key_nonce = ?,
    config_revision = config_revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
WHERE name = ?;

-- name: UpdateConnectionMetadata :execrows
UPDATE connections
SET provider = ?, base_url = ?, extras = ?,
    config_revision = config_revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
WHERE name = ?;

-- name: DeleteConnection :exec
DELETE FROM connections WHERE name = ?;

-- name: GetConnectionMeta :one
SELECT name, provider, base_url, extras, created_at, updated_at, config_revision
FROM connections WHERE name = ?;

-- name: ListConnections :many
SELECT name, provider, base_url, extras, created_at, updated_at, config_revision
FROM connections ORDER BY name;

-- name: GetConnectionCipher :one
SELECT api_key_cipher, api_key_nonce FROM connections WHERE name = ?;

-- name: CountConnections :one
SELECT COUNT(*) FROM connections;
