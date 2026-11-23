package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const mobilityToken = "11111111-1111-1111-1111-111111111111"

func TestMigration044_CandidateAndFenceConstraints(t *testing.T) {
	db := setupTestDB(t).DB
	_, err := db.Exec(`INSERT INTO workspace_homes(run_id,runner_id,generation,updated_at) VALUES('run','builtin',1,'t')`)
	require.NoError(t, err)
	candidate := `INSERT INTO workspace_checkpoints(run_id,version,manifest_hash,archive_path,state,job_id,job_generation,resource_key,resource_owner,resource_generation,resource_token,created_at) VALUES('run',?,?,'archive',?,?,?,'workspace:run','builtin',1,?,'t')`
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err = db.Exec(candidate, 1, hash, "prepared", "job", 1, mobilityToken)
	require.NoError(t, err)
	var version int
	require.NoError(t, db.QueryRow(`SELECT checkpoint_version FROM workspace_homes WHERE run_id='run'`).Scan(&version))
	require.Zero(t, version, "prepared candidate must not publish progress")
	for _, args := range [][]any{
		{2, hash, "invalid", "job", 1, mobilityToken},
		{2, hash, "prepared", nil, nil, mobilityToken},
		{2, hash, "prepared", "job", 0, mobilityToken},
		{2, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "prepared", "job", 1, mobilityToken},
	} {
		_, err = db.Exec(candidate, args...)
		require.Error(t, err)
	}
	_, err = db.Exec(`UPDATE workspace_checkpoints SET state='committed' WHERE run_id='run' AND version=1`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT checkpoint_version FROM workspace_homes WHERE run_id='run'`).Scan(&version))
	require.Zero(t, version, "catalog state alone must not advance home")
	for _, token := range []string{"", "malformed", "00000000-0000-0000-0000-000000000000"} {
		_, err = db.Exec(`INSERT INTO workspace_leases VALUES('workspace:bad','job','builtin',1,?,1)`, token)
		require.Error(t, err)
	}
	_, err = db.Exec(`INSERT INTO workspace_leases VALUES('workspace:run','job','builtin',1,?,1)`, mobilityToken)
	require.NoError(t, err)
	op := `INSERT INTO step_commit_operations(invocation_id,job_id,job_runner_id,job_generation) VALUES('inv',?,'builtin',1)`
	_, err = db.Exec(op, "job-1")
	require.NoError(t, err)
	_, err = db.Exec(op, "job-1")
	require.Error(t, err)
	_, err = db.Exec(op, "job-2")
	require.NoError(t, err)
}

func TestMigration044_RetryProgressSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.db")
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, RunMigrations(db))
	_, err = db.Exec(`INSERT INTO users(id,username) VALUES('owner','mobility-owner')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO instances(id,owner_id,workflow_name,workflow_snapshot,state) VALUES('run','owner','test','{}','running')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO node_invocations(id,run_id,node_id,visit,created_at) VALUES('inv','run','node',1,'t')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO node_executions(id,instance_id,node_id,status) VALUES('exec','run','node','failed')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO node_retry_progress(invocation_id,attempt,retry_not_before,exhausted,last_execution_id) VALUES('inv',2,'later',0,'exec')`)
	require.NoError(t, err)
	failure := `INSERT INTO node_retry_failures(invocation_id,attempt,execution_id,payload_hash) VALUES(?,?,?,?)`
	_, err = db.Exec(failure, "inv", 1, "exec", "hash")
	require.NoError(t, err)
	_, err = db.Exec(failure, "inv", 1, "exec", "other")
	require.Error(t, err)
	_, err = db.Exec(failure, "missing", 2, "exec", "hash")
	require.Error(t, err)
	_, err = db.Exec(failure, "inv", 2, "missing", "hash")
	require.Error(t, err)
	_, err = db.Exec(failure, "inv", 0, "exec", "hash")
	require.Error(t, err)
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	var attempt int
	var deadline, execution string
	var exhausted bool
	require.NoError(t, db.QueryRow(`SELECT attempt,retry_not_before,exhausted,last_execution_id FROM node_retry_progress WHERE invocation_id='inv'`).Scan(&attempt, &deadline, &exhausted, &execution))
	require.Equal(t, 2, attempt)
	require.Equal(t, "later", deadline)
	require.False(t, exhausted)
	require.Equal(t, "exec", execution)
	var failures int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM node_retry_failures WHERE invocation_id='inv'`).Scan(&failures))
	require.Equal(t, 1, failures)
}
