-- name: ReadSystemSettings :one
SELECT catalog.value AS catalog_url, webhook.value AS teams_webhook_url
FROM system_settings AS catalog
JOIN system_settings AS webhook ON webhook.key = 'model_announcements.teams_webhook_url'
WHERE catalog.key = 'model_catalog.url';

-- name: UpdateSystemSetting :execrows
UPDATE system_settings SET value = ? WHERE key = ?;
