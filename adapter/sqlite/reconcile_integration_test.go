package sqlite

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertRowCount asserts the total row count in the given table.
func assertRowCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var n int
	var q string
	switch table {
	case "user_departments":
		q = `SELECT COUNT(*) FROM user_departments`
	default:
		t.Fatalf("assertRowCount: unknown table %q", table)
	}
	require.NoError(t, db.QueryRowContext(context.Background(), q).Scan(&n))
	assert.Equal(t, want, n, "%s total row count", table)
}

// TestReconcileMappings_Integration_ProductionShapedDB exercises the full
// boot-time reconcile path: domain.ReconcileMappings + real DepartmentRenamer
// + SQLite, against a DB seeded with data across all four tables (users,
// user_departments, instances, schedules, published_workflows). This is the
// "startup reconciler against a populated, production-shaped DB" integration
// path from the final-org-gaps review.
func TestReconcileMappings_Integration_ProductionShapedDB(t *testing.T) {
	tdb := setupTestDB(t)
	db := tdb.DB
	ctx := context.Background()

	userA := uuid.New().String()
	userB := uuid.New().String()
	for _, row := range []struct{ id, name string }{{userA, "alice"}, {userB, "bob"}} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`, row.id, row.name)
		require.NoError(t, err)
	}

	// UserA → verbatim "guid-123" (maps to "engineering")
	// UserB → verbatim "guid-456" (maps to "sales") and also "stable-dept" (not in Mapping)
	_, err := db.ExecContext(ctx,
		`INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userA)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-456')`, userB)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_departments (user_id, department) VALUES (?, 'stable-dept')`, userB)
	require.NoError(t, err)

	// Instance rows under both verbatim values.
	for _, row := range []struct{ id, owner, dept string }{
		{uuid.New().String(), userA, "guid-123"},
		{uuid.New().String(), userB, "guid-456"},
	} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, department, visibility)
			 VALUES (?, ?, 'wf', '{}', 'queued', 'normal', '{}', '{}', ?, 'department')`,
			row.id, row.owner, row.dept)
		require.NoError(t, err)
	}

	// Schedule under guid-123.
	_, err = db.ExecContext(ctx,
		`INSERT INTO schedules (id, workflow_name, cron_expr, creator_id, department)
		 VALUES (?, 'wf', '0 0 * * *', ?, 'guid-123')`, uuid.New().String(), userA)
	require.NoError(t, err)

	// Published workflow visibility under both verbatim values.
	_, err = db.ExecContext(ctx,
		`INSERT INTO published_workflows (name, publisher_id, source_name, visibility, department)
		 VALUES ('wf-eng', ?, 'wf', 'department', 'guid-123')`, userA)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO published_workflows (name, publisher_id, source_name, visibility, department)
		 VALUES ('wf-sales', ?, 'wf2', 'department', 'guid-456')`, userB)
	require.NoError(t, err)

	groupCfg := identity.GroupConfig{
		Mapping: map[string]string{
			"guid-123": "engineering",
			"guid-456": "sales",
		},
	}
	renamer := NewDepartmentRenamer(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err = identity.ReconcileMappings(ctx, groupCfg, renamer, logger)
	require.NoError(t, err, "boot-time reconcile must not fail on a production-shaped DB")

	// Verbatim values are gone; friendly names are present everywhere.
	// user_departments: guid-123→engineering, guid-456→sales, stable-dept untouched.
	assertDeptCount(t, db, "user_departments", "engineering", 1)
	assertDeptCount(t, db, "user_departments", "sales", 1)
	assertDeptCount(t, db, "user_departments", "stable-dept", 1)
	assertDeptCount(t, db, "user_departments", "guid-123", 0)
	assertDeptCount(t, db, "user_departments", "guid-456", 0)

	assertDeptCount(t, db, "instances", "engineering", 1)
	assertDeptCount(t, db, "instances", "sales", 1)
	assertDeptCount(t, db, "instances", "guid-123", 0)
	assertDeptCount(t, db, "instances", "guid-456", 0)

	assertDeptCount(t, db, "schedules", "engineering", 1)
	assertDeptCount(t, db, "schedules", "guid-123", 0)

	assertDeptCount(t, db, "published_workflows", "engineering", 1)
	assertDeptCount(t, db, "published_workflows", "sales", 1)
	assertDeptCount(t, db, "published_workflows", "guid-123", 0)
	assertDeptCount(t, db, "published_workflows", "guid-456", 0)
}

// TestReconcileMappings_Integration_Idempotent verifies the full boot path is
// safe to run twice: a server restart with an unchanged Mapping must not
// corrupt the DB or return an error.
func TestReconcileMappings_Integration_Idempotent(t *testing.T) {
	tdb := setupTestDB(t)
	db := tdb.DB
	ctx := context.Background()

	userID := uuid.New().String()
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userID)
	require.NoError(t, err)

	groupCfg := identity.GroupConfig{
		Mapping: map[string]string{"guid-123": "engineering"},
	}
	renamer := NewDepartmentRenamer(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, identity.ReconcileMappings(ctx, groupCfg, renamer, logger), "first boot")
	assertDeptCount(t, db, "user_departments", "engineering", 1)
	assertDeptCount(t, db, "user_departments", "guid-123", 0)

	require.NoError(t, identity.ReconcileMappings(ctx, groupCfg, renamer, logger),
		"second boot (restart with same config) must not fail")
	assertDeptCount(t, db, "user_departments", "engineering", 1)
	assertDeptCount(t, db, "user_departments", "guid-123", 0)
}

// TestReconcileMappings_Integration_EmptyMapping verifies that a clean install
// with no Mapping configured is a cheap no-op: no DB writes, no error, and
// data already in the DB is untouched.
func TestReconcileMappings_Integration_EmptyMapping(t *testing.T) {
	tdb := setupTestDB(t)
	db := tdb.DB
	ctx := context.Background()

	userID := uuid.New().String()
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_departments (user_id, department) VALUES (?, 'verbatim-dept')`, userID)
	require.NoError(t, err)

	// No Mapping configured — common on first boot before any SSO groups are mapped.
	groupCfg := identity.GroupConfig{}
	renamer := NewDepartmentRenamer(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, identity.ReconcileMappings(ctx, groupCfg, renamer, logger),
		"empty Mapping must be a no-op, not an error")

	// Data is unchanged.
	assertDeptCount(t, db, "user_departments", "verbatim-dept", 1)
	assertRowCount(t, db, "user_departments", 1)
}
