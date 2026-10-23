-- name: CreateInstance :one
-- NOTE: InstanceRepo.Create executes this statement HAND-WRITTEN (see
-- createInstanceSQL in instance_repo.go) so the column set can evolve without a
-- sqlc regeneration step, the same way CountActiveByWorkflowID does. This
-- definition is kept in lockstep as the readable source of truth.
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at, department, visibility, trigger_source, schedule_id, workflow_id, parent_instance_id, parent_node_id, spawn_mode, spawn_depth, model_pins, model_pins_version, model_pin_routes, group_run_id, group_task_key, group_task_kind, current_node_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
-- departments. It FAILS CLOSED: admitting every row requires the EXPLICIT
-- `unconstrained` assertion, which the engine service sets only for an admin.
-- A filter set with no owner, no departments, no public and no assertion is a
-- caller with no computed scope and matches nothing.
--
-- The parent narrowing (parent_instance_id IS NULL for roots-only, or
-- parent_instance_id = ? for a specific parent) is appended as a further AND by
-- the Go builder, so it can only NARROW the authorized set, never widen it.
-- Active-descendant discovery additionally uses a recursive CTE and appends
-- id DESC to the created_at order, giving its offset pages a total order.
SELECT id, owner_id, workflow_name, state, priority, current_node_id, failed_node_id, variables, workflow_snapshot, created_at, updated_at, completed_at, queued_at, department, visibility, trigger_source, schedule_id, parent_instance_id, model_pins, model_pins_version, model_pin_routes
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
-- NOTE: executed hand-written by InstanceRepo.ListRunning
-- (listRunningWithLinkSQL over instanceColumnsWithLink) so spawn_depth and the
-- 019 group-task columns are read in the same statement.
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
-- appear in the result - absence means zero.
SELECT parent_instance_id, COUNT(*) AS child_count
FROM instances
WHERE parent_instance_id IN (sqlc.slice('parent_ids'))
GROUP BY parent_instance_id;

-- name: ListActiveChildren :many
-- Every active child regardless of spawn mode. Cancellation traverses
-- ListChildren instead, to reach descendants below terminal intermediates.
SELECT id FROM instances
WHERE parent_instance_id = ?
  AND state IN ('queued','running','waiting','paused')
ORDER BY created_at ASC;

-- name: ListChildren :many
-- Hand-written in InstanceRepo: every child, terminal ones included.
SELECT id, state FROM instances
WHERE parent_instance_id = ?
ORDER BY created_at ASC, rowid ASC;

-- name: ListOrphanedActiveChildren :many
-- Hand-written in InstanceRepo. Climb through completed/failed intermediates;
-- active ancestors stop the climb. The bound guards corrupt cycles.
WITH RECURSIVE up(orphan_id, ancestor_id, hops) AS (
  SELECT c.id, c.parent_instance_id, 1
    FROM instances c
   WHERE c.state IN ('queued','running','waiting','paused')
     AND c.parent_instance_id IS NOT NULL
  UNION ALL
  SELECT up.orphan_id, a.parent_instance_id, up.hops + 1
    FROM up
    JOIN instances a ON a.id = up.ancestor_id
   WHERE a.state IN ('completed','failed')
     AND a.parent_instance_id IS NOT NULL
     AND up.hops < 64
)
SELECT c.id FROM instances c
 WHERE c.id IN (
   SELECT up.orphan_id FROM up
     JOIN instances a ON a.id = up.ancestor_id
     JOIN instance_creation_order o ON o.instance_id = up.orphan_id
     JOIN instance_creation_order boundary ON boundary.instance_id = a.id
    WHERE a.state = 'cancelled'
       OR (a.subtree_cancel_requested_at IS NOT NULL
           AND o.sequence <= boundary.cancel_through_sequence))
 ORDER BY c.created_at ASC, c.rowid ASC;

-- name: MarkSubtreeCancelRequested :exec
-- Hand-written in InstanceRepo. Durable intent without changing the outcome.
-- Migration 021's trigger atomically records the creation-sequence boundary.
UPDATE instances
   SET subtree_cancel_requested_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
 WHERE id = ? AND state IN ('completed','failed','cancelled');

-- name: FindChildBySpawnNode :one
-- The awaited child of ONE (parent, spawn node, mode) triple. All three key
-- parts are load-bearing: without the node id a child of a different spawn node
-- would answer; without the mode a fire-and-forget child would.
SELECT id, state FROM instances
WHERE parent_instance_id = ? AND parent_node_id = ? AND spawn_mode = ?
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: ListWaitingOnTerminalChild :many
-- Startup reconciliation: parents stuck in the waiting state whose awaited
-- child is already terminal (the in-process wake was lost to a crash).
SELECT p.id, p.current_node_id, c.id, c.state
FROM instances p
JOIN instances c ON c.parent_instance_id = p.id
WHERE p.state = 'waiting'
  AND p.current_node_id IS NOT NULL
  AND c.parent_node_id = p.current_node_id
  AND c.spawn_mode = 'wait_for_child'
  AND c.state IN ('completed','failed','cancelled');
