-- name: ListUserDepartments :many
SELECT department FROM user_departments WHERE user_id = ? ORDER BY department;

-- name: DeleteUserDepartments :exec
DELETE FROM user_departments WHERE user_id = ?;

-- name: InsertUserDepartment :exec
INSERT INTO user_departments (user_id, department) VALUES (?, ?)
ON CONFLICT(user_id, department) DO NOTHING;
