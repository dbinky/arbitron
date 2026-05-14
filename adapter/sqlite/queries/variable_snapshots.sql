-- name: CreateVariableSnapshot :one
INSERT INTO variable_snapshots (id, instance_id, node_id, variables)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: ListVariableSnapshotsByInstance :many
SELECT * FROM variable_snapshots WHERE instance_id = ? ORDER BY captured_at;
