package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_SourceRemovalPreservesAssignmentHistory(t *testing.T) {
	repo, db := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	src := subSource(runner.CredServerConnection, "removed-connection", "openai")
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{src}))
	insertJobRow(t, db, "builtin", string(src.ID), "completed")
	var jobID string
	require.NoError(t, db.DB.QueryRow(`SELECT id FROM runner_jobs`).Scan(&jobID))
	_, err := db.DB.Exec(`INSERT INTO runner_job_attempts (job_id, generation, runner_id, source_id, started_at) VALUES (?, 1, 'builtin', ?, '2026-11-20T00:00:00Z')`, jobID, string(src.ID))
	require.NoError(t, err)
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, nil))
	_, err = repo.GetSource(ctx, src.ID)
	require.ErrorIs(t, err, runner.ErrSourceNotFound)
	var jobSource, attemptSource string
	require.NoError(t, db.DB.QueryRow(`SELECT source_id FROM runner_jobs WHERE id=?`, jobID).Scan(&jobSource))
	require.NoError(t, db.DB.QueryRow(`SELECT source_id FROM runner_job_attempts WHERE job_id=?`, jobID).Scan(&attemptSource))
	assert.Equal(t, string(src.ID), jobSource)
	assert.Equal(t, string(src.ID), attemptSource)
}
