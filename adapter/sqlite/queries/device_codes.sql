-- name: CreateDeviceCode :exec
INSERT INTO device_codes (device_code_hash, user_code, status, expires_at)
VALUES (?, ?, 'pending', ?);

-- name: GetDeviceCodeByHash :one
SELECT device_code_hash, user_code, status, user_id, created_at, expires_at
FROM device_codes WHERE device_code_hash = ? LIMIT 1;

-- name: GetDeviceCodeByUserCode :one
SELECT device_code_hash, user_code, status, user_id, created_at, expires_at
FROM device_codes WHERE user_code = ? LIMIT 1;

-- name: ApproveDeviceCode :execrows
UPDATE device_codes SET status = 'approved', user_id = ?
WHERE user_code = ? AND status = 'pending';

-- name: DenyDeviceCode :execrows
UPDATE device_codes SET status = 'denied'
WHERE user_code = ? AND status = 'pending';

-- name: ConsumeDeviceCode :execrows
UPDATE device_codes SET status = 'consumed'
WHERE device_code_hash = ? AND status = 'approved';

-- name: DeleteExpiredDeviceCodes :execrows
DELETE FROM device_codes WHERE expires_at < ?;
