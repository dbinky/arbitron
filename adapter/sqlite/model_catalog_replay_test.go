package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogCycleRejectsJournalIDForAnotherAlias(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	catalog := NewModelCatalogRepo(db.DB)
	cycle := catalogCycle(t, db)
	original, err := catalog.CommitCycle(ctx, cycle)
	require.NoError(t, err)
	require.Len(t, original.Changes, 1)
	profiles := NewProfileRepo(db.DB)
	other := cycle.Advances[0].Expected.Clone()
	other.Alias = "other"
	require.NoError(t, profiles.Create(ctx, other))
	other, err = profiles.Get(ctx, "other")
	require.NoError(t, err)
	cycle.Report.ID = "collision-cycle"
	cycle.Advances[0].Expected = other

	report, err := catalog.CommitCycle(ctx, cycle)
	require.ErrorIs(t, err, provider.ErrAdvanceConflict)
	assert.Empty(t, report)
	got, err := profiles.Get(ctx, "other")
	require.NoError(t, err)
	assert.Equal(t, other, got)
	assert.Zero(t, journalCount(t, db, "other"))
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	view, err := catalog.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, view.Reports, 1, "a colliding replay must roll back the cycle")
	assert.Equal(t, original.ID, view.Reports[0].ID)
}
