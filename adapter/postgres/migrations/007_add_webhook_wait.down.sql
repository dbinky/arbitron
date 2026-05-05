DROP INDEX IF EXISTS idx_instances_webhook_token;
ALTER TABLE instances DROP COLUMN IF EXISTS webhook_received_at;
ALTER TABLE instances DROP COLUMN IF EXISTS webhook_secret_hash;
ALTER TABLE instances DROP COLUMN IF EXISTS webhook_token;
