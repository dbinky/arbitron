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

-- name: ListUsers :many
SELECT id, subject, email, username, role, created_at FROM users ORDER BY created_at;

-- name: UpdateUserRole :exec
UPDATE users SET role = ? WHERE id = ?;

-- name: CountAdmins :one
SELECT COUNT(*) FROM users WHERE role = 'admin';
