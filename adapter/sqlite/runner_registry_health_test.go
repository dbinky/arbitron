package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_InventoryRefreshBlocksUnsupportedWithoutClearingExhaustion(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	src := subSource(runner.CredServerConnection, "production", "openai")
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{src}))
	reset := regNow.Add(time.Hour)
	zero := 0.0
	health := runner.SourceHealth{Revision: 17, Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ResetAt: &reset, Headroom: &zero, ConfirmationModel: "gpt-5.1", LastOutcome: runner.OutcomeExhausted}
	require.NoError(t, repo.RecordHealth(ctx, src.ID, health))
	for _, reason := range []string{"unsupported harness version", ""} {
		src.UnavailableReason = reason
		require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{src}))
		got, err := repo.GetSource(ctx, src.ID)
		require.NoError(t, err)
		assert.Equal(t, reason, got.UnavailableReason)
		assert.Equal(t, health, got.Health)
		snapshot, err := repo.Snapshot(ctx)
		require.NoError(t, err)
		require.Len(t, snapshot.Sources, 1)
		assert.Equal(t, got, snapshot.Sources[0])
	}
}

func TestRegistry_ConfirmationModelRoundTrip(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	src := subSource(runner.CredOpenAISubscription, "", "openai")
	src.Health = runner.SourceHealth{Revision: 3, Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ConfirmationModel: "gpt-5.1"}
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{src}))
	got, err := repo.GetSource(ctx, src.ID)
	require.NoError(t, err)
	assert.Equal(t, src.Health, got.Health)
	next := src.Health
	next.Revision++
	next.ConfirmationModel = "gpt-5.2"
	require.NoError(t, repo.RecordHealth(ctx, src.ID, next))
	got, err = repo.GetSource(ctx, src.ID)
	require.NoError(t, err)
	assert.Equal(t, next, got.Health)
}

func TestMigration041_SourceRevision(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.DB.Exec(`INSERT INTO runners (id, name, created_at, updated_at) VALUES ('builtin', 'builtin', '2026-11-20T00:00:00Z', '2026-11-20T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`INSERT INTO runner_sources (id, runner_id, kind, updated_at) VALUES ('s', 'builtin', 'none', '2026-11-20T00:00:00Z')`)
	require.NoError(t, err)
	var revision int
	require.NoError(t, db.DB.QueryRow(`SELECT revision FROM runner_sources WHERE id='s'`).Scan(&revision))
	assert.Zero(t, revision)
	_, err = db.DB.Exec(`UPDATE runner_sources SET revision=-1 WHERE id='s'`)
	assert.Error(t, err)
}
