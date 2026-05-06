-- name: CreateInstance :one
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetInstance :one
SELECT * FROM instances WHERE id = $1;

-- name: ListInstances :many
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at
FROM instances
WHERE (sqlc.narg('state')::instance_state IS NULL OR state = sqlc.narg('state')::instance_state)
AND (sqlc.narg('workflow_name')::text IS NULL OR workflow_name = sqlc.narg('workflow_name')::text)
AND (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since')::timestamptz)
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: UpdateInstanceState :exec
UPDATE instances SET state = $2, updated_at = now() WHERE id = $1;

-- name: UpdateInstanceVariables :exec
UPDATE instances SET variables = $2, updated_at = now() WHERE id = $1;

-- name: UpdateInstanceCurrentNode :exec
UPDATE instances SET current_node_id = $2, updated_at = now() WHERE id = $1;

-- name: SetInstanceFailed :exec
UPDATE instances SET state = 'failed', failed_node_id = $2, completed_at = now(), updated_at = now() WHERE id = $1;

-- name: SetInstanceCompleted :exec
UPDATE instances SET state = 'completed', completed_at = now(), updated_at = now() WHERE id = $1;

-- name: ListRunningInstances :many
SELECT * FROM instances WHERE state = 'running';

-- name: WorkflowStats :one
SELECT
  COUNT(*) AS total_runs,
  COUNT(*) FILTER (WHERE state = 'completed') AS success_count,
  COUNT(*) FILTER (WHERE state = 'failed') AS failure_count,
  COALESCE(EXTRACT(EPOCH FROM AVG(completed_at - created_at)) FILTER (WHERE state = 'completed' AND completed_at IS NOT NULL), 0) AS avg_duration_secs
FROM instances
WHERE workflow_name = $1
  AND created_at >= $2
  AND created_at <= $3;
