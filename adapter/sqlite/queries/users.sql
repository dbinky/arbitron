-- name: GetDefaultUser :one
SELECT id, subject, email, username, role, created_at FROM users WHERE username = 'local' LIMIT 1;

-- name: GetUserByID :one
SELECT id, subject, email, username, role, created_at FROM users WHERE id = ? LIMIT 1;

-- name: GetUserBySubject :one
SELECT id, subject, email, username, role, created_at FROM users WHERE subject = ? LIMIT 1;

-- name: CreateUser :exec
INSERT INTO users (id, subject, email, username, role) VALUES (?, ?, ?, ?, ?);

-- name: UpdateUsername :exec
UPDATE users SET username = ? WHERE id = ?;
