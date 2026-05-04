CREATE TABLE variable_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    variables JSONB NOT NULL DEFAULT '{}',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_variable_snapshots_instance_captured ON variable_snapshots (instance_id, captured_at);
