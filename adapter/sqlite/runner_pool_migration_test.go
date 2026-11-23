package sqlite

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration041_CreatesPoolTables(t *testing.T) {
	db := setupTestDB(t)
	for _, table := range []string{"runners", "runner_sources", "runner_jobs", "runner_job_attempts", "workspace_homes", "pool_events"} {
		var n int
		require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n))
		assert.Equal(t, 1, n, "table %s", table)
	}
	var applied int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 41`).Scan(&applied))
	assert.Equal(t, 1, applied)
}

func TestMigration041_Constraints(t *testing.T) {
	db := setupTestDB(t)
	now := "2026-11-20T00:00:00Z"
	_, err := db.DB.Exec(`INSERT INTO runners (id, name, step_limit, created_at, updated_at) VALUES ('r0', 'r0', 0, ?, ?)`, now, now)
	assert.Error(t, err, "step_limit must be >= 1")
	_, err = db.DB.Exec(`INSERT INTO runners (id, name, step_limit, created_at, updated_at) VALUES ('r1', 'r1', 257, ?, ?)`, now, now)
	assert.Error(t, err, "step_limit must be <= 256")
	_, err = db.DB.Exec(`INSERT INTO runners (id, name, step_limit, created_at, updated_at) VALUES ('builtin', 'builtin', 8, ?, ?)`, now, now)
	require.NoError(t, err)

	var compat, liveness string
	require.NoError(t, db.DB.QueryRow(`SELECT compat_state, liveness FROM runners WHERE id='builtin'`).Scan(&compat, &liveness))
	assert.Equal(t, "compatible", compat)
	assert.Equal(t, "offline", liveness)

	_, err = db.DB.Exec(`INSERT INTO runner_sources (id, runner_id, kind, credential_ref, updated_at) VALUES ('builtin/server_connection/a', 'builtin', 'server_connection', 'a', ?)`, now)
	require.NoError(t, err)
	_, err = db.DB.Exec(`INSERT INTO runner_sources (id, runner_id, kind, credential_ref, updated_at) VALUES ('dup', 'builtin', 'server_connection', 'a', ?)`, now)
	assert.Error(t, err, "one source per (runner, kind, credential_ref)")
	_, err = db.DB.Exec(`INSERT INTO runner_sources (id, runner_id, kind, credential_ref, updated_at) VALUES ('x', 'nope', 'server_connection', 'b', ?)`, now)
	assert.Error(t, err, "sources belong to a registered runner")

	run, inv := uuid.NewString(), uuid.NewString()
	insert := `INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind, state, created_at, updated_at)
	           VALUES (?, ?, ?, 'node', 1, 'n', 'chat', 'anthropic_subscription', ?, ?, ?)`
	_, err = db.DB.Exec(insert, uuid.NewString(), run, inv, "queued", now, now)
	require.NoError(t, err)
	_, err = db.DB.Exec(insert, uuid.NewString(), run, inv, "queued", now, now)
	assert.Error(t, err, "the idempotency key is unique")
	_, err = db.DB.Exec(insert, uuid.NewString(), uuid.NewString(), inv, "bogus", now, now)
	assert.Error(t, err, "state is constrained")

	var headroom *float64
	require.NoError(t, db.DB.QueryRow(`SELECT headroom FROM runner_sources WHERE id='builtin/server_connection/a'`).Scan(&headroom))
	assert.Nil(t, headroom, "unknown headroom is NULL, never 0")
}
