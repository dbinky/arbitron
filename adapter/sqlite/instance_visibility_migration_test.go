package sqlite

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration: the instances table gains department + visibility columns after
// RunMigrations, and a pre-existing-style row reads back visibility='private'
// with a NULL department (owner-only behavior, matching spec §Migration).
func TestMigration012_InstanceVisibilityColumns(t *testing.T) {
	db := setupTestDB(t)

	// Insert an instance the way pre-012 code would (no visibility/department),
	// relying on the column defaults supplied by migration 012.
	id := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority)
		 VALUES (?, '00000000-0000-0000-0000-000000000001', 'wf', '{}', 'queued', 'normal')`,
		id,
	)
	require.NoError(t, err)

	var visibility string
	var department *string
	require.NoError(t, db.DB.QueryRow(
		`SELECT visibility, department FROM instances WHERE id = ?`, id,
	).Scan(&visibility, &department))

	assert.Equal(t, "private", visibility, "existing rows default to private (owner-only)")
	assert.Nil(t, department, "department defaults to NULL (default group / private)")
}

// The check constraint rejects an unknown visibility value.
func TestMigration012_VisibilityCheckConstraint(t *testing.T) {
	db := setupTestDB(t)
	id := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, visibility)
		 VALUES (?, '00000000-0000-0000-0000-000000000001', 'wf', '{}', 'queued', 'normal', 'bogus')`,
		id,
	)
	require.Error(t, err, "visibility CHECK must reject values outside private|department|public")
}

// The dashboard/authz index exists after migration.
func TestMigration012_IndexExists(t *testing.T) {
	db := setupTestDB(t)
	var name string
	require.NoError(t, db.DB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_instances_dept_vis'`,
	).Scan(&name))
	assert.Equal(t, "idx_instances_dept_vis", name)
}
