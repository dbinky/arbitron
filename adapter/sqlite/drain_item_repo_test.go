// adapter/sqlite/drain_item_repo_test.go
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

var _ runner.DrainStore = (*DrainItemRepo)(nil)

func TestDrainItemRepo_UpsertListClear(t *testing.T) {
	ctx := context.Background()
	repo := NewDrainItemRepo(setupTestDB(t).DB)
	run1, run2 := uuid.New(), uuid.New()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Upsert(ctx, runner.DrainItem{RunnerID: "mac-1", Kind: runner.DrainKindWorkspace, RunID: run1, State: runner.DrainItemPending, Reason: "waiting", UpdatedAt: at}))
	require.NoError(t, repo.Upsert(ctx, runner.DrainItem{RunnerID: "mac-1", Kind: runner.DrainKindWorkspace, RunID: run1, GroupKey: "anthropic_subscription:anthropic",
		State: runner.DrainItemBlocked, Reason: "no eligible runner for anthropic_subscription:anthropic", UpdatedAt: at.Add(time.Minute)}))
	require.NoError(t, repo.Upsert(ctx, runner.DrainItem{RunnerID: "mac-1", Kind: runner.DrainKindWorkspace, RunID: run2, State: runner.DrainItemDone, UpdatedAt: at}))
	require.NoError(t, repo.Upsert(ctx, runner.DrainItem{RunnerID: "mac-2", Kind: runner.DrainKindWorkspace, RunID: run1, State: runner.DrainItemPending, UpdatedAt: at}))

	items, err := repo.List(ctx, "mac-1")
	require.NoError(t, err)
	require.Len(t, items, 2, "one row per (runner, kind, run)")
	var blocked runner.DrainItem
	for _, it := range items {
		if it.RunID == run1 {
			blocked = it
		}
	}
	assert.Equal(t, runner.DrainItemBlocked, blocked.State)
	assert.Equal(t, "anthropic_subscription:anthropic", blocked.GroupKey)
	assert.Contains(t, blocked.Reason, "no eligible runner")
	assert.True(t, blocked.UpdatedAt.Equal(at.Add(time.Minute)))

	require.NoError(t, repo.Clear(ctx, "mac-1"))
	items, err = repo.List(ctx, "mac-1")
	require.NoError(t, err)
	assert.Empty(t, items)
	items, _ = repo.List(ctx, "mac-2")
	assert.Len(t, items, 1, "other runners are untouched")

	require.Error(t, repo.Upsert(ctx, runner.DrainItem{RunnerID: "mac-1", Kind: "printer", RunID: run1, State: runner.DrainItemPending, UpdatedAt: at}),
		"the schema rejects unknown kinds")
}
