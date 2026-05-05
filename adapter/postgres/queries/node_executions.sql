-- name: CreateNodeExecution :one
INSERT INTO node_executions (id, instance_id, node_id, attempt, status, resolved_inputs, output, error, llm_conversation, llm_model, llm_tokens_in, llm_tokens_out, script_stdout, script_stderr, script_exit_code, started_at, completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING *;

-- name: UpdateNodeExecution :exec
UPDATE node_executions SET status = $2, output = $3, error = $4, llm_conversation = $5, llm_model = $6, llm_tokens_in = $7, llm_tokens_out = $8, script_stdout = $9, script_stderr = $10, script_exit_code = $11, completed_at = $12
WHERE id = $1;

-- name: ListNodeExecutionsByInstance :many
SELECT * FROM node_executions WHERE instance_id = $1 ORDER BY started_at;

-- name: GetNodeExecutionByInstanceAndNode :one
SELECT * FROM node_executions WHERE instance_id = $1 AND node_id = $2 ORDER BY attempt DESC LIMIT 1;
