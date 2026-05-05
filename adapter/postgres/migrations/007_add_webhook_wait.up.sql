ALTER TABLE instances ADD COLUMN webhook_token TEXT;
ALTER TABLE instances ADD COLUMN webhook_secret_hash TEXT;
ALTER TABLE instances ADD COLUMN webhook_received_at TIMESTAMPTZ;

-- Unique index on webhook_token for fast lookup and uniqueness enforcement
CREATE UNIQUE INDEX idx_instances_webhook_token ON instances (webhook_token)
    WHERE webhook_token IS NOT NULL;
