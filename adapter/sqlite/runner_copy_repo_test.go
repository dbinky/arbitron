package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ runner.CopyInventory = (*RunnerCopyRepo)(nil)

func TestRunnerCopyRepo_Lifecycle(t *testing.T) {
	ctx := context.Background()
	repo := NewRunnerCopyRepo(setupTestDB(t).DB)
	run := uuid.New()

	require.NoError(t, repo.RecordHeld(ctx, run, "mac-1", 1))
	require.NoError(t, repo.RecordHeld(ctx, run, "mac-2", 2))
	require.NoError(t, repo.MarkForDeletion(ctx, run, "mac-1", 1, runner.CopyDeletePending))

	all, err := repo.ListByRun(ctx, run)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, runner.RunnerID("mac-1"), all[0].RunnerID)
	assert.Equal(t, runner.CopyDeletePending, all[0].Status)
	assert.Equal(t, runner.CopyActive, all[1].Status)
	assert.Equal(t, int64(2), all[1].HomeGeneration)

	pend, err := repo.PendingDeletes(ctx, "mac-1")
	require.NoError(t, err)
	require.Len(t, pend, 1)
	assert.Equal(t, run, pend[0].RunID)

	require.NoError(t, repo.MarkDeleted(ctx, run, "mac-1"))
	pend, err = repo.PendingDeletes(ctx, "mac-1")
	require.NoError(t, err)
	assert.Empty(t, pend)
	all, err = repo.ListByRun(ctx, run)
	require.NoError(t, err)
	assert.Len(t, all, 1, "deleted copies are not listed")

	require.NoError(t, repo.MarkDeleted(ctx, run, "mac-2"))
	all, _ = repo.ListByRun(ctx, run)
	assert.Equal(t, runner.CopyActive, all[0].Status, "an active copy is never marked deleted")
}

func TestRunnerCopyRepo_NeverDowngradesANewerActiveCopy(t *testing.T) {
	ctx := context.Background()
	repo := NewRunnerCopyRepo(setupTestDB(t).DB)
	run := uuid.New()
	require.NoError(t, repo.RecordHeld(ctx, run, "mac-1", 5))
	require.NoError(t, repo.MarkForDeletion(ctx, run, "mac-1", 3, runner.CopyStale))
	all, err := repo.ListByRun(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.CopyActive, all[0].Status, "a late delete for generation 3 cannot touch generation 5")
	assert.Equal(t, int64(5), all[0].HomeGeneration)

	require.NoError(t, repo.MarkForDeletion(ctx, run, "mac-1", 6, runner.CopyDeletePending))
	all, _ = repo.ListByRun(ctx, run)
	assert.Equal(t, runner.CopyDeletePending, all[0].Status)
	assert.Equal(t, int64(6), all[0].HomeGeneration)

	require.ErrorIs(t, repo.MarkForDeletion(ctx, run, "mac-1", 6, runner.CopyActive), runner.ErrInvalidCopyStatus)
}
