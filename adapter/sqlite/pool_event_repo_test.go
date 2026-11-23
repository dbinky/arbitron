package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func poolEvent(kind string, at time.Time, run *uuid.UUID) runner.PoolEvent {
	return runner.PoolEvent{ID: uuid.New(), At: at, Kind: kind, RunnerID: runner.BuiltInRunnerID,
		SourceID: "builtin/anthropic_subscription/-", RunID: run,
		Cause:  runner.Cause{Kind: runner.CauseOutcome, Actor: "", Outcome: runner.OutcomeExhausted},
		Detail: map[string]any{"group": "anthropic_subscription:anthropic", "generation": float64(2)}}
}

func TestPoolEventRepo_AppendListNewestFirst(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPoolEventRepo(db.DB)
	ctx := context.Background()
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	run := uuid.New()
	first := poolEvent("source.health_changed", at, nil)
	second := poolEvent("job.rerouted", at, &run) // same second: order comes from seq
	require.NoError(t, repo.Append(ctx, first))
	require.NoError(t, repo.Append(ctx, second))

	got, err := repo.List(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, second, got[0], "newest first, full round trip including run scope and detail")
	assert.Equal(t, first, got[1])
	assert.Nil(t, got[1].RunID, "system-scoped events have no run")

	one, err := repo.List(ctx, 1)
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, second.ID, one[0].ID)
}

func TestPoolEventRepo_PruneKeepsNewest(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPoolEventRepo(db.DB)
	ctx := context.Background()
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		ev := poolEvent("runner.updated", time.Date(2026, 11, 20, 10, 0, i, 0, time.UTC), nil)
		ids = append(ids, ev.ID)
		require.NoError(t, repo.Append(ctx, ev))
	}
	require.NoError(t, repo.Prune(ctx, 2))
	got, err := repo.List(ctx, 100)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, ids[4], got[0].ID)
	assert.Equal(t, ids[3], got[1].ID)
	require.NoError(t, repo.Prune(ctx, 10), "keeping more than exist is a no-op")
	assert.Error(t, repo.Prune(ctx, -1))
}

func TestPoolEventRepo_RejectsInvalidEvents(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPoolEventRepo(db.DB)
	ev := poolEvent("runner.updated", time.Now(), nil)
	ev.ID = uuid.Nil
	assert.Error(t, repo.Append(context.Background(), ev))
	ev = poolEvent("", time.Now(), nil)
	assert.Error(t, repo.Append(context.Background(), ev))
}

func TestPoolEventRepo_ListDefaultLimit(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPoolEventRepo(db.DB)
	for i := 0; i < 105; i++ {
		require.NoError(t, repo.Append(context.Background(), poolEvent("runner.updated", time.Now(), nil)))
	}
	got, err := repo.List(context.Background(), 0)
	require.NoError(t, err)
	assert.Len(t, got, 100)
}
