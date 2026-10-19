-- Reverse 019, children first. Leave the four added instances columns in
-- place: forward-only column removal is the convention (migrations/README.md).
-- Rebuilding instances with foreign_keys=ON would delete node_executions.
DROP TRIGGER IF EXISTS group_task_terminal_insert;
DROP TRIGGER IF EXISTS group_task_terminal_update;
DROP TRIGGER IF EXISTS group_task_terminal_immutable;
DROP TABLE IF EXISTS group_terminal_clock;
DROP INDEX IF EXISTS idx_group_tasks_terminal_seq;
DROP TABLE IF EXISTS group_tasks;
DROP INDEX IF EXISTS idx_group_runs_unsettled;
DROP TABLE IF EXISTS group_runs;
DROP INDEX IF EXISTS idx_instances_group_run;
