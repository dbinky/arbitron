package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tableColumns(t *testing.T, db testDB, table string) map[string]bool {
	t.Helper()
	rows, err := db.DB.Query("SELECT name FROM pragma_table_info(?)", table)
	require.NoError(t, err)
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		cols[name] = true
	}
	require.NoError(t, rows.Err())
	return cols
}

func TestMigration045_CreatesSessionTablesAndColumns(t *testing.T) {
	db := setupTestDB(t)

	leases := tableColumns(t, db, "turn_leases")
	for _, c := range []string{"key", "token", "generation", "expires_at", "holder", "boot_id"} {
		assert.True(t, leases[c], "turn_leases.%s", c)
	}
	intents := tableColumns(t, db, "turn_intents")
	for _, c := range []string{"id", "lease_key", "run_id", "invocation_id", "node_id", "turn_index", "kind", "prompt",
		"system_prompt", "nonce", "message", "model", "tools", "prior_checkpoint_ref", "session_generation",
		"operation_id", "request_id", "accepted_binding", "created_at", "completed_at"} {
		assert.True(t, intents[c], "turn_intents.%s", c)
	}
	moves := tableColumns(t, db, "session_moves")
	for _, c := range []string{"id", "run_id", "invocation_id", "node_id", "from_runner", "to_runner", "reason",
		"lossy", "notice", "generation", "at"} {
		assert.True(t, moves[c], "session_moves.%s", c)
	}
	execs := tableColumns(t, db, "node_executions")
	for _, c := range []string{"session_invocation_id", "session_checkpoint_ref", "session_pin_runner", "session_generation"} {
		assert.True(t, execs[c], "node_executions.%s", c)
	}

	res := tableColumns(t, db, "resolution_resources")
	for _, c := range []string{"id", "group_run_id", "merge_index", "attempt", "task_key", "source_dir", "work_dir",
		"state", "materialized_ref", "created_at", "updated_at"} {
		assert.True(t, res[c], "resolution_resources.%s", c)
	}

	var version int
	require.NoError(t, db.DB.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.GreaterOrEqual(t, version, 45)
}

func TestMigration045_RejectsUnknownMoveReasonAndIntentKind(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.DB.Exec(`INSERT INTO session_moves (id, run_id, invocation_id, node_id, from_runner, to_runner, reason, lossy, notice, generation, at)
VALUES ('m1','r','i','n','a','b','load',0,'',2,0)`)
	assert.Error(t, err, "moves for load are never allowed")
	_, err = db.DB.Exec(`INSERT INTO turn_intents (id, lease_key, run_id, invocation_id, node_id, turn_index, kind, created_at)
VALUES ('t1','run:x','r','i','n',1,'bogus',0)`)
	assert.Error(t, err)
}
