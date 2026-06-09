-- NOTE: sqlc was not available in the build environment when this feature
-- landed, so adapter/sqlite/schedule_repo.go is hand-written with direct
-- database/sql calls (consistent with notification_repo.go). These queries are
-- kept in sync with that repo so a later `sqlc generate` is a drop-in.

-- name: CreateSchedule :exec
INSERT INTO schedules (id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSchedule :one
SELECT id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables, last_fired_at, created_at, updated_at
FROM schedules WHERE id = ?;

-- name: ListSchedules :many
SELECT id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables, last_fired_at, created_at, updated_at
FROM schedules
WHERE (sqlc.narg('creator_id')    IS NULL OR creator_id    = sqlc.narg('creator_id'))
  AND (sqlc.narg('workflow_name') IS NULL OR workflow_name = sqlc.narg('workflow_name'))
  AND (sqlc.narg('enabled')       IS NULL OR enabled       = sqlc.narg('enabled'))
ORDER BY created_at DESC, id;

-- name: ListEnabledSchedules :many
SELECT id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables, last_fired_at, created_at, updated_at
FROM schedules WHERE enabled = 1 ORDER BY id;

-- name: UpdateSchedule :exec
UPDATE schedules
SET workflow_name = ?, cron_expr = ?, timezone = ?, department = ?, enabled = ?, variables = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
WHERE id = ?;

-- name: SetScheduleLastFired :exec
UPDATE schedules SET last_fired_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

-- name: DeleteSchedule :exec
DELETE FROM schedules WHERE id = ?;
