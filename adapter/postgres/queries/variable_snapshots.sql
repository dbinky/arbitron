-- name: CreateVariableSnapshot :one
INSERT INTO variable_snapshots (id, instance_id, node_id, variables)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListVariableSnapshotsByInstance :many
SELECT * FROM variable_snapshots WHERE instance_id = $1 ORDER BY captured_at;
