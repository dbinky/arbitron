-- name: CreateInstance :one
-- NOTE: InstanceRepo.Create executes this statement HAND-WRITTEN (see
-- createInstanceSQL in instance_repo.go) so the column set can evolve without a
-- sqlc regeneration step, the same way CountActiveByWorkflowID does. This
-- definition is kept in lockstep as the readable source of truth.
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at, department, visibility, trigger_source, schedule_id, workflow_id, parent_instance_id, parent_node_id, spawn_mode)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetInstance :one
-- NOTE: executed hand-written by InstanceRepo.Get (instanceColumnsWithLink) so
-- the spawn-link columns are read in the same statement.
SELECT * FROM instances WHERE id = ?;

-- name: ListInstances :many
-- NOTE: InstanceRepo.List assembles this statement in Go so the optional parent
-- narrowing can be appended without a sqlc regeneration step. This definition
-- is kept in lockstep as the readable source of truth for the RBAC clause.
--
-- Disjunctive RBAC visibility filter: a caller sees a row when it is owned by
-- them, OR public (when include_public), OR a department run in one of their
-- departments. An admin passes owner_id=NULL, include_public=0, and an empty
-- departments array, in which case the guarded `unconstrained` arm admits every
-- row.
--
-- The parent narrowing (parent_instance_id IS NULL for roots-only, or
-- parent_instance_id = ? for a specific parent) is appended as a further AND by
-- the Go builder, so it can only NARROW the authorized set, never widen it.
SELECT id, owner_id, workflow_name, state, priority, current_node_id, failed_node_id, variables, workflow_snapshot, created_at, updated_at, completed_at, queued_at, department, visibility, trigger_source, schedule_id, parent_instance_id
FROM instances
WHERE (sqlc.narg('state') IS NULL OR state = sqlc.narg('state'))
AND (sqlc.narg('workflow_name') IS NULL OR workflow_name = sqlc.narg('workflow_name'))
AND (sqlc.narg('since') IS NULL OR created_at >= sqlc.narg('since'))
AND (
    sqlc.arg('unconstrained') = 1
    OR (sqlc.narg('owner_id') IS NOT NULL AND owner_id = sqlc.narg('owner_id'))
    OR (sqlc.arg('include_public') = 1 AND visibility = 'public')
    OR (visibility = 'department' AND department IN (
        SELECT value FROM json_each(sqlc.arg('departments'))
    ))
)
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateInstanceState :exec
UPDATE instances SET state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

-- name: UpdateInstanceVariables :exec
UPDATE instances SET variables = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

-- name: UpdateInstanceCurrentNode :exec
UPDATE instances SET current_node_id = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

-- name: SetInstanceFailed :exec
UPDATE instances SET state = 'failed', failed_node_id = ?, completed_at = strftime('%Y-%m-%dT%H:%M:%SZ'), updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

-- name: SetInstanceCompleted :exec
UPDATE instances SET state = 'completed', completed_at = strftime('%Y-%m-%dT%H:%M:%SZ'), updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

-- name: ListRunningInstances :many
SELECT * FROM instances WHERE state = 'running';

-- name: UpdateInstancePriority :exec
UPDATE instances SET priority = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

-- name: SetWaitingWithTimer :exec
UPDATE instances SET state = 'waiting', waiting_until = ?, updated_at = ? WHERE id = ?;

-- name: ClearWaitingUntil :exec
-- Clears waiting_until without changing state. The caller is responsible for
-- state transitions (e.g. back to 'running'). Matches Postgres adapter behavior.
UPDATE instances SET waiting_until = NULL, updated_at = ? WHERE id = ?;

-- name: SetWebhookFields :exec
UPDATE instances SET webhook_token = ?, webhook_secret_hash = ?, updated_at = ? WHERE id = ?;

-- name: SetWebhookReceived :exec
UPDATE instances SET webhook_received_at = ?, updated_at = ? WHERE id = ?;

-- name: ChildCountsByParents :many
-- NOTE: executed hand-written by InstanceRepo.ChildCountsByParents (the IN list
-- and the caller's visibility disjunction are both variable-arity). Kept here as
-- the readable source of truth. Parents with no visible children simply do not
-- appear in the result — absence means zero.
SELECT parent_instance_id, COUNT(*) AS child_count
FROM instances
WHERE parent_instance_id IN (sqlc.slice('parent_ids'))
GROUP BY parent_instance_id;

-- name: ListActiveChildren :many
-- Every active child of a parent, REGARDLESS of spawn mode: the cancel cascade
-- covers awaited and fire-and-forget children alike.
SELECT id FROM instances
WHERE parent_instance_id = ?
  AND state IN ('queued','running','waiting','paused')
ORDER BY created_at ASC;
