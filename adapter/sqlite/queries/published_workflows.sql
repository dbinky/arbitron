-- name: GetPublishedWorkflow :one
SELECT name, publisher_id, source_name, published_at, visibility, department
FROM published_workflows
WHERE name = ?;

-- name: UpsertPublishedWorkflow :exec
INSERT INTO published_workflows (name, publisher_id, source_name, visibility, department)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    publisher_id = excluded.publisher_id,
    source_name  = excluded.source_name,
    visibility   = excluded.visibility,
    department   = excluded.department,
    published_at = strftime('%Y-%m-%dT%H:%M:%SZ');

-- name: ListPublishedWorkflows :many
SELECT name, publisher_id, source_name, published_at, visibility, department
FROM published_workflows
ORDER BY name;

-- name: DeletePublishedWorkflow :exec
DELETE FROM published_workflows WHERE name = ?;
