-- name: CreateInstance :one
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetInstance :one
SELECT * FROM instances WHERE id = ?;

-- name: ListInstances :many
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at
FROM instances
WHERE (sqlc.narg('state') IS NULL OR state = sqlc.narg('state'))
AND (sqlc.narg('workflow_name') IS NULL OR workflow_name = sqlc.narg('workflow_name'))
AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateInstanceState :exec
UPDATE instances SET state = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateInstanceVariables :exec
UPDATE instances SET variables = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateInstanceCurrentNode :exec
UPDATE instances SET current_node_id = ?, updated_at = datetime('now') WHERE id = ?;

-- name: SetInstanceFailed :exec
UPDATE instances SET state = 'failed', failed_node_id = ?, completed_at = datetime('now'), updated_at = datetime('now') WHERE id = ?;

-- name: SetInstanceCompleted :exec
UPDATE instances SET state = 'completed', completed_at = datetime('now'), updated_at = datetime('now') WHERE id = ?;

-- name: ListRunningInstances :many
SELECT * FROM instances WHERE state = 'running';

-- name: UpdateInstancePriority :exec
UPDATE instances SET priority = ?, updated_at = datetime('now') WHERE id = ?;
