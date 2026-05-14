-- name: CountRunning :one
SELECT COUNT(*) FROM instances WHERE state = 'running';

-- name: CountRunningByOwner :one
SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = ?;

-- name: CountQueued :one
SELECT COUNT(*) FROM instances WHERE state = 'queued';

-- name: CountWaiting :one
SELECT COUNT(*) FROM instances WHERE state = 'waiting';

-- name: NextEligible :one
SELECT * FROM instances i
WHERE i.state = 'queued'
  AND (sqlc.arg('global_cap') = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running') < sqlc.arg('global_cap'))
  AND (sqlc.arg('per_user_cap') = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = i.owner_id) < sqlc.arg('per_user_cap'))
ORDER BY
  CASE i.priority
    WHEN 'high' THEN 1
    WHEN 'normal' THEN 2
    WHEN 'low' THEN 3
  END,
  i.queued_at ASC
LIMIT 1;

-- name: ListWaitingWithTimer :many
SELECT * FROM instances WHERE state = 'waiting' AND waiting_until IS NOT NULL AND webhook_token IS NULL;

-- name: ListWaitingWithWebhook :many
SELECT * FROM instances WHERE state = 'waiting' AND webhook_token IS NOT NULL;

-- name: SetQueued :exec
UPDATE instances SET state = 'queued', queued_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;
