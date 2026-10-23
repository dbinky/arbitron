-- name: ModelStateSecretInitialized :one
SELECT secret_initialized FROM model_state_meta WHERE id = 1;

-- name: MarkModelStateSecretInitialized :exec
UPDATE model_state_meta SET secret_initialized = 1 WHERE id = 1;

-- name: InsertModelStateKeyVerifier :exec
INSERT INTO model_state_key_verifier (id, verifier) VALUES (1, ?) ON CONFLICT (id) DO NOTHING;

-- name: ModelStateKeyVerifier :one
SELECT verifier FROM model_state_key_verifier WHERE id = 1;
