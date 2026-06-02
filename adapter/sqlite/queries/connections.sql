-- name: CreateConnection :exec
INSERT INTO connections (name, provider, base_url, extras, api_key_cipher, api_key_nonce)
VALUES (?, ?, ?, ?, ?, ?);

-- name: UpdateConnectionWithKey :exec
UPDATE connections
SET provider = ?, base_url = ?, extras = ?, api_key_cipher = ?, api_key_nonce = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
WHERE name = ?;

-- name: UpdateConnectionMetadata :exec
UPDATE connections
SET provider = ?, base_url = ?, extras = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
WHERE name = ?;

-- name: DeleteConnection :exec
DELETE FROM connections WHERE name = ?;

-- name: GetConnectionMeta :one
SELECT name, provider, base_url, extras, created_at, updated_at
FROM connections WHERE name = ?;

-- name: ListConnections :many
SELECT name, provider, base_url, extras, created_at, updated_at
FROM connections ORDER BY name;

-- name: GetConnectionCipher :one
SELECT api_key_cipher, api_key_nonce FROM connections WHERE name = ?;

-- name: CountConnections :one
SELECT COUNT(*) FROM connections;
