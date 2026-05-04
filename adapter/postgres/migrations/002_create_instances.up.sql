DO $$ BEGIN
  CREATE TYPE instance_state AS ENUM ('queued', 'running', 'completed', 'failed', 'cancelled', 'paused');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
  CREATE TYPE instance_priority AS ENUM ('high', 'normal', 'low');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL REFERENCES users(id),
    workflow_name TEXT NOT NULL,
    workflow_snapshot JSONB NOT NULL,
    state instance_state NOT NULL DEFAULT 'queued',
    priority instance_priority NOT NULL DEFAULT 'normal',
    variables JSONB NOT NULL DEFAULT '{}',
    original_variables JSONB NOT NULL DEFAULT '{}',
    current_node_id TEXT,
    failed_node_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
