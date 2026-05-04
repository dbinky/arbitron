CREATE TYPE node_execution_status AS ENUM ('succeeded', 'failed', 'skipped');

CREATE TABLE node_executions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 1,
    status node_execution_status NOT NULL,
    resolved_inputs JSONB,
    output JSONB,
    error JSONB,
    llm_conversation JSONB,
    llm_model TEXT,
    llm_tokens_in INTEGER,
    llm_tokens_out INTEGER,
    script_stdout TEXT,
    script_stderr TEXT,
    script_exit_code INTEGER,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_node_executions_instance_started ON node_executions (instance_id, started_at);
