package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var mobilityTables = []string{
	"workspace_checkpoints", "home_moves", "transfer_sessions",
	"step_commits", "drain_items", "runner_copies", "workspace_leases",
	"step_commit_operations", "builtin_processes", "step_suspensions", "node_retry_progress", "node_retry_failures",
}

func TestMigration044_WorkspaceMobility(t *testing.T) {
	db := setupTestDB(t).DB
	var applied int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = 44`).Scan(&applied))
	require.Equal(t, 1, applied)
	for _, tbl := range mobilityTables {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n))
		assert.Equal(t, 1, n, tbl)
	}

	// workspace_checkpoints: (run_id, version) unique; version >= 1; 64-hex hash.
	ins := `INSERT INTO workspace_checkpoints(run_id,version,manifest_hash,archive_path,size_bytes,state,resource_key,resource_owner,resource_generation,resource_token,created_at) VALUES(?,?,?,?,?,'committed','workspace:run-1','builtin',1,'11111111-1111-1111-1111-111111111111',?)`
	hash := "0000000000000000000000000000000000000000000000000000000000000000"
	_, err := db.Exec(ins, "run-1", 1, hash, "/c/v1.tar.gz", 10, "2026-11-20T00:00:00Z")
	require.NoError(t, err)
	_, err = db.Exec(ins, "run-1", 1, hash, "/c/v1b.tar.gz", 10, "2026-11-20T00:00:00Z")
	require.Error(t, err, "duplicate version rejected")
	_, err = db.Exec(ins, "run-1", 0, hash, "/c/v0.tar.gz", 10, "2026-11-20T00:00:00Z")
	require.Error(t, err, "version 0 rejected")
	_, err = db.Exec(ins, "run-1", 2, "short", "/c/v2.tar.gz", 10, "2026-11-20T00:00:00Z")
	require.Error(t, err, "non-sha256 hash rejected")

	// home_moves: one row per (run, generation).
	mv := `INSERT INTO home_moves(run_id,from_runner,to_runner,reason,bytes,generation,moved_at) VALUES(?,?,?,?,?,?,?)`
	_, err = db.Exec(mv, "run-1", "", "builtin", "acquire", 0, 1, "2026-11-20T00:00:00Z")
	require.NoError(t, err)
	_, err = db.Exec(mv, "run-1", "builtin", "mac-1", "must_move", 5, 1, "2026-11-20T00:00:01Z")
	require.Error(t, err)

	// transfer_sessions: direction vocabulary + JSON parts.
	ts := `INSERT INTO transfer_sessions(token_hash,job_id,generation,runner_id,run_id,direction,expires_at,parts,resource_key,resource_owner,resource_generation,resource_token,created_at) VALUES(?,?,?,?,?,?,?,?,'workspace:run-1','builtin',1,'11111111-1111-1111-1111-111111111111',?)`
	_, err = db.Exec(ts, hash, "job", 1, "mac-1", "run-1", "push", "2026-11-20T00:10:00Z", "{}", "2026-11-20T00:00:00Z")
	require.NoError(t, err)
	_, err = db.Exec(ts, "1"+hash[1:], "job", 1, "mac-1", "run-1", "sideways", "2026-11-20T00:10:00Z", "{}", "2026-11-20T00:00:00Z")
	require.Error(t, err)
	_, err = db.Exec(ts, "2"+hash[1:], "job", 1, "mac-1", "run-1", "pull", "2026-11-20T00:10:00Z", "not json", "2026-11-20T00:00:00Z")
	require.Error(t, err)

	// step_commits: invocation is the key.
	sc := `INSERT INTO step_commits(invocation_id,run_id,node_id,execution_id,next_node_id,variables,committed_at) VALUES(?,?,?,?,?,?,?)`
	_, err = db.Exec(sc, "inv-1", "run-1", "A", "exec-1", "B", "{}", "2026-11-20T00:00:00Z")
	require.NoError(t, err)
	_, err = db.Exec(sc, "inv-1", "run-1", "A", "exec-2", "B", "{}", "2026-11-20T00:00:00Z")
	require.Error(t, err)

	// drain_items / runner_copies vocabularies.
	_, err = db.Exec(`INSERT INTO drain_items(runner_id,kind,run_id,state,updated_at) VALUES('mac-1','workspace','run-1','blocked','t')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO drain_items(runner_id,kind,run_id,state,updated_at) VALUES('mac-1','printer','run-1','blocked','t')`)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO runner_copies(run_id,runner_id,home_generation,state,updated_at) VALUES('run-1','mac-1',2,'active','t')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO runner_copies(run_id,runner_id,home_generation,state,updated_at) VALUES('run-2','mac-1',2,'zombie','t')`)
	require.Error(t, err)

	// Non-portable marker defaults empty and survives rollback of the extension.
	_, err = db.Exec(`INSERT INTO workspace_homes(run_id,runner_id,generation,updated_at) VALUES('run-1','builtin',1,'t')`)
	require.NoError(t, err)
	var reason string
	require.NoError(t, db.QueryRow(`SELECT non_portable_reason FROM workspace_homes WHERE run_id='run-1'`).Scan(&reason))
	require.Empty(t, reason)
	var cols int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('workspace_homes') WHERE name='non_portable_reason'`).Scan(&cols))
	require.Equal(t, 1, cols)

	// Down then up again is clean.
	down, err := migrationsFS.ReadFile("migrations/044_workspace_mobility.down.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	var homes int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM workspace_homes WHERE run_id='run-1'`).Scan(&homes))
	require.Equal(t, 1, homes)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('workspace_homes') WHERE name='non_portable_reason'`).Scan(&cols))
	require.Zero(t, cols)
	for _, tbl := range mobilityTables {
		_, err = db.Exec(`SELECT * FROM ` + tbl)
		require.Error(t, err, tbl)
	}
	_, err = db.Exec(`DELETE FROM schema_migrations WHERE version=44`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	for _, tbl := range mobilityTables {
		_, err = db.Exec(`SELECT * FROM ` + tbl)
		require.NoError(t, err, tbl)
	}
}
