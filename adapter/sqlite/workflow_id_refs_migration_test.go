package sqlite

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration 017 up: instances, schedules, and published_workflows each gain a
// nullable workflow_id column. A row inserted the pre-017 way (no workflow_id)
// reads back NULL — additive, no behavior change.
func TestMigration017_WorkflowIDColumns(t *testing.T) {
	db := setupTestDB(t)

	owner := "00000000-0000-0000-0000-000000000001"

	instID := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables)
		 VALUES (?, ?, 'wf', '{}', 'queued', 'normal', '{}', '{}')`,
		instID, owner,
	)
	require.NoError(t, err)

	schedID := uuid.New().String()
	_, err = db.DB.Exec(
		`INSERT INTO schedules (id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables)
		 VALUES (?, 'wf', '0 * * * *', 'UTC', ?, '', 1, '{}')`,
		schedID, owner,
	)
	require.NoError(t, err)

	var publisherID string
	require.NoError(t, db.DB.QueryRow(`SELECT id FROM users WHERE username = 'local'`).Scan(&publisherID))
	_, err = db.DB.Exec(
		`INSERT INTO published_workflows (name, publisher_id, source_name) VALUES ('wf', ?, 'wf')`,
		publisherID,
	)
	require.NoError(t, err)

	var instWID, schedWID, pubWID *string
	require.NoError(t, db.DB.QueryRow(`SELECT workflow_id FROM instances WHERE id = ?`, instID).Scan(&instWID))
	require.NoError(t, db.DB.QueryRow(`SELECT workflow_id FROM schedules WHERE id = ?`, schedID).Scan(&schedWID))
	require.NoError(t, db.DB.QueryRow(`SELECT workflow_id FROM published_workflows WHERE name = 'wf'`).Scan(&pubWID))

	assert.Nil(t, instWID, "legacy instance row reads workflow_id NULL")
	assert.Nil(t, schedWID, "legacy schedule row reads workflow_id NULL")
	assert.Nil(t, pubWID, "legacy publication row reads workflow_id NULL")
}

// Migration 017 up: the two workflow_id indexes exist after migration.
func TestMigration017_IndexesExist(t *testing.T) {
	db := setupTestDB(t)
	for _, idx := range []string{"idx_instances_workflow_id", "idx_schedules_workflow_id"} {
		var name string
		require.NoError(t, db.DB.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name = ?`, idx,
		).Scan(&name), "index %s must exist after migration 017", idx)
		assert.Equal(t, idx, name)
	}
}

// Migration 017 down: drops the workflow_id indexes only; the columns themselves
// survive (forward-only convention, mirroring 012/014). Executes without error.
func TestMigration017_Down_DropsIndexesOnly(t *testing.T) {
	db := setupTestDB(t)

	// Execute 017 down SQL (same statements as 017_workflow_id_refs.down.sql).
	_, err := db.DB.Exec(`DROP INDEX IF EXISTS idx_instances_workflow_id`)
	require.NoError(t, err, "017 down migration must execute without error")
	_, err = db.DB.Exec(`DROP INDEX IF EXISTS idx_schedules_workflow_id`)
	require.NoError(t, err, "017 down migration must execute without error")

	var count int
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('idx_instances_workflow_id','idx_schedules_workflow_id')`,
	).Scan(&count))
	assert.Equal(t, 0, count, "workflow_id indexes must be absent after 017 down migration")

	// The columns themselves survive — forward-only convention preserves stored data.
	_, err = db.DB.Exec(`SELECT workflow_id FROM instances LIMIT 0`)
	assert.NoError(t, err)
	_, err = db.DB.Exec(`SELECT workflow_id FROM schedules LIMIT 0`)
	assert.NoError(t, err)
	_, err = db.DB.Exec(`SELECT workflow_id FROM published_workflows LIMIT 0`)
	assert.NoError(t, err)
}
