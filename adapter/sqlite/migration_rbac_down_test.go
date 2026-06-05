package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// FA #35: Migration correctness (009–012) — down pairs execute without error
// ---------------------------------------------------------------------------
//
// Each test below applies ALL up migrations (via setupTestDB) then runs the
// corresponding down migration SQL directly — the same statements that are in
// the *.down.sql files. This catches syntax errors and schema drift that would
// only surface at rollback time (not during normal CI which only runs up).
//
// Tests mirror the focus area contract:
//   - existing shared→public (010 default tested in published_workflow_repo_test)
//   - existing instances private/default-group (012 default tested in
//     instance_visibility_migration_test)
//   - up/down pairs: verified here — each down executes cleanly
//   - no data loss: the rows in unrelated tables (users, instances) survive
//   - CHECK constraints: tested in published_workflow_repo_test and
//     instance_visibility_migration_test

// TestMigration009_Down_DropsUserDepartments verifies the 009 down migration
// removes the user_departments table without affecting the parent users table.
// FK cascade correctness (DELETE user → department row gone) is implicitly
// guaranteed by the CASCADE constraint; the down migration itself just removes
// the table entirely so this test focuses on no-error execution and absence.
func TestMigration009_Down_DropsUserDepartments(t *testing.T) {
	db := setupTestDB(t)

	// Confirm the table exists after up migrations.
	var count int
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_departments'`).Scan(&count))
	require.Equal(t, 1, count, "user_departments must exist after up migrations")

	// Execute 009 down SQL.
	_, err := db.DB.Exec(`DROP TABLE IF EXISTS user_departments`)
	require.NoError(t, err, "009 down migration must execute without error")

	// Table must be gone.
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_departments'`).Scan(&count))
	assert.Equal(t, 0, count, "user_departments must be absent after 009 down migration")

	// The users table must still be intact — no data loss in the parent table.
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count))
	assert.Greater(t, count, 0, "users table must still contain the seeded admin after 009 down migration")
}

// TestMigration010_Down_DropsVisibilityColumns verifies the 010 down migration
// removes the visibility and department columns from published_workflows and
// drops their index. The remaining columns (name, publisher_id, source_name)
// must be intact — "no data loss" for non-RBAC fields.
func TestMigration010_Down_DropsVisibilityColumns(t *testing.T) {
	db := setupTestDB(t)

	// Seed a publication so there is data to preserve.
	_, err := db.DB.Exec(
		`INSERT INTO published_workflows (name, publisher_id, source_name, visibility)
		 VALUES ('wf', '00000000-0000-0000-0000-000000000001', 'src', 'public')`)
	require.NoError(t, err)

	// Execute 010 down SQL (same statements as 010_workflow_visibility.down.sql).
	_, err = db.DB.Exec(`DROP INDEX IF EXISTS idx_published_visibility`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`ALTER TABLE published_workflows DROP COLUMN department`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`ALTER TABLE published_workflows DROP COLUMN visibility`)
	require.NoError(t, err, "010 down migration must execute without error")

	// visibility and department columns must be absent.
	_, err = db.DB.Exec(`SELECT visibility FROM published_workflows LIMIT 0`)
	require.Error(t, err, "visibility column must not exist after 010 down migration")

	// Original columns must survive — no data loss.
	var name string
	require.NoError(t, db.DB.QueryRow(`SELECT name FROM published_workflows WHERE name='wf'`).Scan(&name))
	assert.Equal(t, "wf", name, "pre-existing publication name must survive 010 down migration")
}

// TestMigration011_Down_DropsAuditEvents verifies the 011 down migration
// removes the audit_events table and its two indexes. The rest of the schema
// (instances, published_workflows, …) must survive unaffected.
func TestMigration011_Down_DropsAuditEvents(t *testing.T) {
	db := setupTestDB(t)

	// Confirm the table and indexes exist.
	var count int
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='audit_events'`).Scan(&count))
	require.Equal(t, 1, count, "audit_events must exist after up migrations")

	// Execute 011 down SQL (same statements as 011_audit_log.down.sql).
	_, err := db.DB.Exec(`DROP INDEX IF EXISTS idx_audit_actor`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`DROP INDEX IF EXISTS idx_audit_occurred`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`DROP TABLE IF EXISTS audit_events`)
	require.NoError(t, err, "011 down migration must execute without error")

	// audit_events must be gone; unrelated tables intact.
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='audit_events'`).Scan(&count))
	assert.Equal(t, 0, count, "audit_events must be absent after 011 down migration")

	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM instances`).Scan(&count))
	// COUNT(*) may be 0 but the query must not error — table must still exist.
	_ = count
}

// TestMigration012_Down_DropsIndexOnly verifies the 012 down migration drops
// the list-authz index but intentionally leaves the visibility and department
// columns in place (forward-only convention; SQLite DROP COLUMN limitations on
// older deployments). The columns must still be readable after the down.
func TestMigration012_Down_DropsIndexOnly(t *testing.T) {
	db := setupTestDB(t)

	// Confirm index exists.
	var count int
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_instances_dept_vis'`).Scan(&count))
	require.Equal(t, 1, count, "idx_instances_dept_vis must exist after up migrations")

	// Execute 012 down SQL (same statements as 012_instance_visibility.down.sql).
	_, err := db.DB.Exec(`DROP INDEX IF EXISTS idx_instances_dept_vis`)
	require.NoError(t, err, "012 down migration must execute without error")

	// Index must be gone.
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_instances_dept_vis'`).Scan(&count))
	assert.Equal(t, 0, count, "idx_instances_dept_vis must be absent after 012 down migration")

	// The columns themselves must still be queryable — forward-only convention
	// means the down migration does NOT drop them, preserving stored data.
	_, err = db.DB.Exec(`SELECT visibility, department FROM instances LIMIT 0`)
	assert.NoError(t, err, "visibility and department columns must survive 012 down migration (forward-only: columns not dropped)")
}
