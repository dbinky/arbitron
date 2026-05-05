-- name: CountRunning :one
SELECT COUNT(*) FROM instances WHERE state = 'running';

-- name: CountRunningByOwner :one
SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = $1;

-- name: CountQueued :one
SELECT COUNT(*) FROM instances WHERE state = 'queued';

-- name: CountWaiting :one
SELECT COUNT(*) FROM instances WHERE state = 'waiting';

-- name: NextEligible :one
SELECT * FROM instances i
WHERE i.state = 'queued'
  AND ($1::int = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running') < $1)
  AND ($2::int = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = i.owner_id) < $2)
ORDER BY
  CASE i.priority
    WHEN 'high' THEN 1
    WHEN 'normal' THEN 2
    WHEN 'low' THEN 3
  END,
  i.queued_at ASC
LIMIT 1;

-- name: SetQueued :exec
UPDATE instances SET state = 'queued', queued_at = $2, updated_at = now() WHERE id = $1;

-- name: UpdateInstancePriority :exec
UPDATE instances SET priority = $2, updated_at = now() WHERE id = $1;
