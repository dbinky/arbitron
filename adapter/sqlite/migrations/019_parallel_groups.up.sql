-- Parallel workflow gathering: durable group runs and ordered task rosters.
-- Never rebuild instances: with foreign_keys=ON that would cascade-delete
-- node_executions. Keep the 018 spawn_mode CHECK unchanged; group tasks store
-- spawn_mode NULL and are decoded from their group link instead. Legacy spawn
-- queries cannot match them, while ancestry queries still include them.
-- Add key/kind first so the group_run_id CHECK can reference them.
ALTER TABLE instances ADD COLUMN group_task_key TEXT;
ALTER TABLE instances ADD COLUMN group_task_kind TEXT
    CHECK (group_task_kind IS NULL OR group_task_kind IN ('branch','workflow'));
-- No FK: purging a group run must not delete a child's execution history.
ALTER TABLE instances ADD COLUMN group_run_id TEXT
    CHECK (group_run_id IS NULL OR (
        spawn_mode IS NULL
        AND parent_instance_id IS NOT NULL AND parent_instance_id <> ''
        AND parent_node_id IS NOT NULL AND parent_node_id <> ''
        AND group_task_key IS NOT NULL AND group_task_key <> ''
        AND group_task_kind IS NOT NULL
    ));
CREATE INDEX IF NOT EXISTS idx_instances_group_run ON instances (group_run_id);

-- Durable subtree-cancellation intent on terminal runs (P2-T12). Same timestamp
-- layout as instances.created_at, so lexical comparison is chronological.
ALTER TABLE instances ADD COLUMN subtree_cancel_requested_at TEXT;

-- No FK on parent_instance_id: purging a parent must preserve group history.
-- Workflow vocabulary is validated upstream, not CHECK-constrained here, so
-- extending it never requires a table rebuild that would cascade-delete tasks.
-- JSON snapshots default to objects; decision/final_output NULL means unset.
-- Repository timestamps use fixed-width UTC RFC3339 with nanoseconds.
CREATE TABLE IF NOT EXISTS group_runs (
    id                     TEXT PRIMARY KEY,
    parent_instance_id     TEXT NOT NULL CHECK (parent_instance_id <> ''),
    node_id                TEXT NOT NULL CHECK (node_id <> ''),
    attempt                INTEGER NOT NULL CHECK (attempt >= 1),
    form                   TEXT NOT NULL,
    completion_rule        TEXT NOT NULL,
    unfinished_behavior    TEXT NOT NULL,
    gather_mode            TEXT NOT NULL,
    config                 TEXT NOT NULL DEFAULT '{}',
    body_ownership         TEXT NOT NULL DEFAULT '{}',
    start_variables        TEXT NOT NULL DEFAULT '{}',
    workspace_snapshot     TEXT NOT NULL DEFAULT '',
    state                  TEXT NOT NULL CHECK (state IN
        ('launching','running','decided','gathering','resolving_conflicts','done')),
    decision               TEXT,
    final_output           TEXT,
    continuation_port      TEXT NOT NULL DEFAULT ''
        CHECK (continuation_port IN ('','success','failure')),
    continuation_ready     INTEGER NOT NULL DEFAULT 0 CHECK (continuation_ready IN (0,1)),
    continuation_delivered INTEGER NOT NULL DEFAULT 0 CHECK (continuation_delivered IN (0,1)),
    actions_applied        INTEGER NOT NULL DEFAULT 0 CHECK (actions_applied IN (0,1)),
    launch_error           TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    UNIQUE (parent_instance_id, node_id, attempt),
    CHECK (decision IS NULL OR state NOT IN ('launching','running')),
    CHECK (final_output IS NULL OR state = 'done'),
    CHECK (continuation_ready = 0 OR final_output IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_group_runs_unsettled ON group_runs (created_at, id)
    WHERE state <> 'done' OR continuation_delivered = 0 OR actions_applied = 0;

-- Child IDs are reserved before enqueue, so they cannot reference instances.
-- terminal_seq is global and assigned atomically with the child's terminal
-- state, never by callback order or wall-clock timestamps.
-- 'unfinished' is frozen-only and is never a live_status. Migration 020 adds
-- finalized_ref; it is deliberately absent here.
CREATE TABLE IF NOT EXISTS group_tasks (
    group_run_id             TEXT NOT NULL REFERENCES group_runs(id) ON DELETE CASCADE,
    position                 INTEGER NOT NULL CHECK (position >= 0),
    task_key                 TEXT NOT NULL CHECK (task_key <> ''),
    kind                     TEXT NOT NULL CHECK (kind IN ('branch','workflow')),
    origin                   TEXT NOT NULL DEFAULT '',
    child_instance_id        TEXT NOT NULL CHECK (child_instance_id <> ''),
    launch_state             TEXT NOT NULL DEFAULT 'pending'
        CHECK (launch_state IN ('pending','launched','launch_failed')),
    launch_error             TEXT NOT NULL DEFAULT '',
    live_status              TEXT NOT NULL DEFAULT 'not_started' CHECK (live_status IN
        ('not_started','queued','running','waiting','paused','completed','failed','cancelled')),
    terminal_seq             INTEGER CHECK (terminal_seq IS NULL OR terminal_seq >= 1),
    terminal_at              TEXT,
    result                   TEXT,
    failure                  TEXT,
    cancel_requested         INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0,1)),
    blocking                 INTEGER NOT NULL DEFAULT 1 CHECK (blocking IN (0,1)),
    continuing_independently INTEGER NOT NULL DEFAULT 0 CHECK (continuing_independently IN (0,1)),
    updated_at               TEXT NOT NULL,
    PRIMARY KEY (group_run_id, position),
    UNIQUE (group_run_id, task_key),
    UNIQUE (child_instance_id),
    CHECK ((terminal_seq IS NULL) = (terminal_at IS NULL)),
    CHECK ((live_status IN ('completed','failed','cancelled')) = (terminal_seq IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_group_tasks_terminal_seq ON group_tasks (terminal_seq);

-- A durable allocator, shared with receipt seeding for childless adapter fixtures.
-- Deleting history cannot rewind this clock. SQLite serializes state writers;
-- each receipt and its child state become visible in the same commit.
CREATE TABLE group_terminal_clock (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    value INTEGER NOT NULL CHECK (value >= 0)
);
INSERT INTO group_terminal_clock (singleton, value) VALUES (1, 0);

CREATE TRIGGER group_task_terminal_immutable
BEFORE UPDATE OF state ON instances
WHEN OLD.group_run_id IS NOT NULL
 AND OLD.state IN ('completed','failed','cancelled') AND NEW.state <> OLD.state
BEGIN
    SELECT RAISE(ABORT, 'group task terminal state is immutable');
END;

CREATE TRIGGER group_task_terminal_insert
AFTER INSERT ON instances
WHEN NEW.group_run_id IS NOT NULL
 AND NEW.state IN ('completed','failed','cancelled')
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM group_tasks WHERE child_instance_id = NEW.id
          AND group_run_id = NEW.group_run_id AND task_key = NEW.group_task_key
    ) THEN RAISE(ABORT, 'group task terminal has no matching roster') END;
    SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM group_terminal_clock WHERE singleton = 1)
        THEN RAISE(ABORT, 'group terminal clock is missing') END;
    UPDATE group_terminal_clock SET value = value + 1 WHERE singleton = 1 AND EXISTS (
        SELECT 1 FROM group_tasks WHERE child_instance_id = NEW.id
          AND group_run_id = NEW.group_run_id AND task_key = NEW.group_task_key
          AND terminal_seq IS NULL
    );
    UPDATE group_tasks SET
        terminal_seq = (SELECT value FROM group_terminal_clock WHERE singleton = 1),
        terminal_at = COALESCE(NEW.completed_at, NEW.updated_at),
        live_status = NEW.state,
        result = json_remove(NEW.variables, '$.workspace'),
        failure = CASE WHEN NEW.state = 'completed' THEN NULL ELSE COALESCE((
            SELECT json_object(
                'state', NEW.state,
                'failedNodeId', COALESCE(NEW.failed_node_id, ne.node_id, ''),
                'error', COALESCE(CASE WHEN json_valid(ne.error)
                    THEN json_extract(ne.error, '$.error') END, 'task ' || NEW.state),
                'attempt', ne.attempt)
            FROM node_executions ne
            WHERE ne.instance_id = NEW.id AND ne.status = 'failed'
              AND (NEW.failed_node_id IS NULL OR ne.node_id = NEW.failed_node_id)
            ORDER BY ne.attempt DESC, ne.started_at DESC, ne.id DESC LIMIT 1
        ), json_object('state', NEW.state, 'failedNodeId', COALESCE(NEW.failed_node_id, ''),
            'error', 'task ' || NEW.state || ' without a failed node execution', 'attempt', 0)) END,
        updated_at = NEW.updated_at
    WHERE child_instance_id = NEW.id AND group_run_id = NEW.group_run_id
      AND task_key = NEW.group_task_key AND terminal_seq IS NULL;
END;

-- Keep this capture body identical to the INSERT trigger. SQLite does not
-- support a shared trigger function; both raw queue SQL and repository writes
-- must hit this same atomic boundary. A replay never overwrites a receipt.
CREATE TRIGGER group_task_terminal_update
AFTER UPDATE OF state ON instances
WHEN NEW.group_run_id IS NOT NULL
 AND OLD.state NOT IN ('completed','failed','cancelled')
 AND NEW.state IN ('completed','failed','cancelled')
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM group_tasks WHERE child_instance_id = NEW.id
          AND group_run_id = NEW.group_run_id AND task_key = NEW.group_task_key
    ) THEN RAISE(ABORT, 'group task terminal has no matching roster') END;
    SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM group_terminal_clock WHERE singleton = 1)
        THEN RAISE(ABORT, 'group terminal clock is missing') END;
    UPDATE group_terminal_clock SET value = value + 1 WHERE singleton = 1 AND EXISTS (
        SELECT 1 FROM group_tasks WHERE child_instance_id = NEW.id
          AND group_run_id = NEW.group_run_id AND task_key = NEW.group_task_key
          AND terminal_seq IS NULL
    );
    UPDATE group_tasks SET
        terminal_seq = (SELECT value FROM group_terminal_clock WHERE singleton = 1),
        terminal_at = COALESCE(NEW.completed_at, NEW.updated_at),
        live_status = NEW.state,
        result = json_remove(NEW.variables, '$.workspace'),
        failure = CASE WHEN NEW.state = 'completed' THEN NULL ELSE COALESCE((
            SELECT json_object(
                'state', NEW.state,
                'failedNodeId', COALESCE(NEW.failed_node_id, ne.node_id, ''),
                'error', COALESCE(CASE WHEN json_valid(ne.error)
                    THEN json_extract(ne.error, '$.error') END, 'task ' || NEW.state),
                'attempt', ne.attempt)
            FROM node_executions ne
            WHERE ne.instance_id = NEW.id AND ne.status = 'failed'
              AND (NEW.failed_node_id IS NULL OR ne.node_id = NEW.failed_node_id)
            ORDER BY ne.attempt DESC, ne.started_at DESC, ne.id DESC LIMIT 1
        ), json_object('state', NEW.state, 'failedNodeId', COALESCE(NEW.failed_node_id, ''),
            'error', 'task ' || NEW.state || ' without a failed node execution', 'attempt', 0)) END,
        updated_at = NEW.updated_at
    WHERE child_instance_id = NEW.id AND group_run_id = NEW.group_run_id
      AND task_key = NEW.group_task_key AND terminal_seq IS NULL;
END;
