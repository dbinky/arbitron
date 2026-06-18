-- name: RegisterWorkflow :exec
INSERT INTO workflow_identity (id, name, scope, owner_id, archived)
VALUES (?, ?, ?, ?, ?);

-- name: ResolveWorkflowID :one
SELECT id FROM workflow_identity WHERE name = ?;

-- name: ResolveWorkflowName :one
SELECT name FROM workflow_identity WHERE id = ?;

-- name: ListWorkflowIdentities :many
SELECT id, name, scope, owner_id, archived
FROM workflow_identity ORDER BY name;

-- name: RenameWorkflow :execrows
UPDATE workflow_identity SET name = ? WHERE id = ?;

-- name: SetArchived :execrows
UPDATE workflow_identity SET archived = ? WHERE id = ?;

-- name: SetOwner :execrows
UPDATE workflow_identity SET owner_id = ? WHERE id = ?;

-- name: DeleteWorkflowIdentity :execrows
DELETE FROM workflow_identity WHERE id = ?;
