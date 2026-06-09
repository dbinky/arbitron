package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenameDepartment_RewritesAllFourTables(t *testing.T) {
	tdb := setupTestDB(t)
	db := tdb.DB
	ctx := context.Background()
	r := NewDepartmentRenamer(db)

	// Seed a user so user_departments FK is satisfied.
	userID := uuid.New().String()
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err)

	// 1) user_departments membership under the verbatim value.
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userID)
	require.NoError(t, err)

	// 2) instance run stamped under verbatim.
	instID := uuid.New().String()
	_, err = db.ExecContext(ctx,
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, department, visibility)
		 VALUES (?, ?, 'wf', '{}', 'queued', 'normal', '{}', '{}', 'guid-123', 'department')`, instID, userID)
	require.NoError(t, err)

	// 3) schedule attributed to verbatim.
	schedID := uuid.New().String()
	_, err = db.ExecContext(ctx,
		`INSERT INTO schedules (id, workflow_name, cron_expr, creator_id, department)
		 VALUES (?, 'wf', '0 0 * * *', ?, 'guid-123')`, schedID, userID)
	require.NoError(t, err)

	// 4) department-visibility published workflow under verbatim.
	_, err = db.ExecContext(ctx,
		`INSERT INTO published_workflows (name, publisher_id, source_name, visibility, department)
		 VALUES ('wf', ?, 'wf', 'department', 'guid-123')`, userID)
	require.NoError(t, err)

	rows, err := r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err)
	assert.Equal(t, int64(4), rows, "one row rewritten in each of the four tables")

	// Every store now reads the friendly name; none retain the verbatim value.
	assertDeptCount(t, db, "user_departments", "engineering", 1)
	assertDeptCount(t, db, "user_departments", "guid-123", 0)
	assertDeptCount(t, db, "instances", "engineering", 1)
	assertDeptCount(t, db, "schedules", "engineering", 1)
	assertDeptCount(t, db, "published_workflows", "engineering", 1)
}

func TestRenameDepartment_Idempotent(t *testing.T) {
	tdb := setupTestDB(t)
	db := tdb.DB
	ctx := context.Background()
	r := NewDepartmentRenamer(db)

	userID := uuid.New().String()
	_, err := db.ExecContext(ctx, `INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userID)
	require.NoError(t, err)

	rows, err := r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// Second run: values are already friendly => WHERE matches nothing => 0 rows.
	rows, err = r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows, "idempotent: a re-run rewrites nothing")
	assertDeptCount(t, db, "user_departments", "engineering", 1)
}

func TestRenameDepartment_MembershipCollision_DropsDuplicate(t *testing.T) {
	// A user already a member of BOTH the verbatim and the friendly value: the
	// UPDATE OR IGNORE keeps the existing friendly membership, drops the verbatim
	// duplicate (composite PK collision). No error, no duplicate.
	tdb := setupTestDB(t)
	db := tdb.DB
	ctx := context.Background()
	r := NewDepartmentRenamer(db)

	userID := uuid.New().String()
	_, err := db.ExecContext(ctx, `INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_departments (user_id, department) VALUES (?, 'engineering')`, userID)
	require.NoError(t, err)

	_, err = r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err, "a PK collision must not fail the rename")
	assertDeptCount(t, db, "user_departments", "engineering", 1)
	assertDeptCount(t, db, "user_departments", "guid-123", 0)
}

// assertDeptCount asserts the number of rows in table whose department column
// equals dept. A literal query per table avoids identifier interpolation.
func assertDeptCount(t *testing.T, db *sql.DB, table, dept string, want int) {
	t.Helper()
	var q string
	switch table {
	case "user_departments":
		q = `SELECT COUNT(*) FROM user_departments WHERE department = ?`
	case "instances":
		q = `SELECT COUNT(*) FROM instances WHERE department = ?`
	case "schedules":
		q = `SELECT COUNT(*) FROM schedules WHERE department = ?`
	case "published_workflows":
		q = `SELECT COUNT(*) FROM published_workflows WHERE department = ?`
	default:
		t.Fatalf("unknown table %q", table)
	}
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), q, dept).Scan(&n))
	assert.Equal(t, want, n, "%s.department=%q count", table, dept)
}
