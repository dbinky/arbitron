CREATE TYPE notification_type AS ENUM ('needs_interaction', 'failed', 'completed');

CREATE TABLE notifications (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id   UUID NOT NULL REFERENCES instances(id),
    type          notification_type NOT NULL,
    read          BOOLEAN NOT NULL DEFAULT false,
    dismissed     BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_active ON notifications (read, dismissed, created_at DESC);
CREATE INDEX idx_notifications_instance ON notifications (instance_id);
