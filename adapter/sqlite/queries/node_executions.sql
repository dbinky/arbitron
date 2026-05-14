-- name: CreateNodeExecution :one
INSERT INTO node_executions (id, instance_id, node_id, attempt, status, resolved_inputs, output, error, llm_conversation, llm_model, llm_tokens_in, llm_tokens_out, script_stdout, script_stderr, script_exit_code, started_at, completed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateNodeExecution :exec
UPDATE node_executions SET status = ?, output = ?, error = ?, llm_conversation = ?, llm_model = ?, llm_tokens_in = ?, llm_tokens_out = ?, script_stdout = ?, script_stderr = ?, script_exit_code = ?, completed_at = ?
WHERE id = ?;

-- name: ListNodeExecutionsByInstance :many
SELECT * FROM node_executions WHERE instance_id = ? ORDER BY started_at;

-- name: GetNodeExecutionByInstanceAndNode :one
SELECT * FROM node_executions WHERE instance_id = ? AND node_id = ? ORDER BY attempt DESC LIMIT 1;
