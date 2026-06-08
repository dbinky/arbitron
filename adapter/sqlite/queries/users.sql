-- name: GetDefaultUser :one
SELECT id, subject, email, username, role, created_at FROM users WHERE username = 'local' LIMIT 1;

-- name: GetUserByID :one
SELECT id, subject, email, username, role, created_at FROM users WHERE id = ? LIMIT 1;

-- name: GetUserBySubject :one
SELECT id, subject, email, username, role, created_at FROM users WHERE subject = ? LIMIT 1;

-- name: GetUserByEmail :one
-- Case-insensitive lookup: the column is lower()'d and the caller passes an
-- ALREADY-lowercased email (see UserRepo.GetByEmail). An empty/NULL email must
-- never match — the repo short-circuits "" before querying. lower(email)=? is
-- used rather than lower(?) / COLLATE NOCASE because the sqlc SQLite parser
-- miscounts tokens for a function-wrapped placeholder and corrupts the query set.
SELECT id, subject, email, username, role, created_at FROM users WHERE lower(email) = ? LIMIT 1;

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
