package sqlite

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration: the instances table gains trigger_source + schedule_id columns
// after RunMigrations. A pre-existing-style row (inserted without the column)
// reads back trigger_source='manual' (the DEFAULT backfills legacy rows) and a
// NULL schedule_id. No foreign key is enforced on schedule_id.
func TestMigration014_TriggerOriginColumns(t *testing.T) {
	db := setupTestDB(t)

	// Insert an instance the way pre-014 code would (no trigger_source/schedule_id),
	// relying on the column defaults supplied by migration 014.
	id := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables)
		 VALUES (?, '00000000-0000-0000-0000-000000000001', 'wf', '{}', 'queued', 'normal', '{}', '{}')`,
		id,
	)
	require.NoError(t, err)

	var triggerSource string
	var scheduleID *string
	require.NoError(t, db.DB.QueryRow(
		`SELECT trigger_source, schedule_id FROM instances WHERE id = ?`, id,
	).Scan(&triggerSource, &scheduleID))

	assert.Equal(t, "manual", triggerSource, "existing/legacy rows backfill to 'manual' via the column DEFAULT")
	assert.Nil(t, scheduleID, "schedule_id defaults to NULL for manual runs")
}

// schedule_id accepts a UUID string (no FK to violate — a deleted schedule's
// historical run keeps its stored id).
func TestMigration014_ScheduleIDAcceptsUUID(t *testing.T) {
	db := setupTestDB(t)
	id := uuid.New().String()
	sid := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, trigger_source, schedule_id)
		 VALUES (?, '00000000-0000-0000-0000-000000000001', 'wf', '{}', 'queued', 'normal', '{}', '{}', 'schedule', ?)`,
		id, sid,
	)
	require.NoError(t, err)

	var triggerSource string
	var scheduleID *string
	require.NoError(t, db.DB.QueryRow(
		`SELECT trigger_source, schedule_id FROM instances WHERE id = ?`, id,
	).Scan(&triggerSource, &scheduleID))

	assert.Equal(t, "schedule", triggerSource)
	require.NotNil(t, scheduleID)
	assert.Equal(t, sid, *scheduleID)
}

// The check constraint rejects an unknown trigger_source value.
func TestMigration014_TriggerSourceCheckConstraint(t *testing.T) {
	db := setupTestDB(t)
	id := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, trigger_source)
		 VALUES (?, '00000000-0000-0000-0000-000000000001', 'wf', '{}', 'queued', 'normal', '{}', '{}', 'bogus')`,
		id,
	)
	require.Error(t, err, "trigger_source CHECK must reject values outside manual|schedule|run_now")
}

// The schedule-linkage index exists after migration.
func TestMigration014_ScheduleIndexExists(t *testing.T) {
	db := setupTestDB(t)
	var name string
	require.NoError(t, db.DB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_instances_schedule_id'`,
	).Scan(&name))
	assert.Equal(t, "idx_instances_schedule_id", name)
}
