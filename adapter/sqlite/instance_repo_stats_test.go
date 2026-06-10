package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstanceRepo_Stats_NoRuns guards the zero-run case: with no instances of a
// workflow in the window, COUNT(*) is 0 but the SUM(CASE...) aggregates evaluate
// to NULL. Scanning NULL into the non-nullable int fields must not error — the
// stats must come back as all-zero. This is the Usage Stats 500 a freshly seeded
// workflow (never run) triggered.
func TestInstanceRepo_Stats_NoRuns(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	from := time.Now().UTC().AddDate(0, 0, -30)
	to := time.Now().UTC()

	stats, err := repo.Stats(ctx, "never-run", from, to)
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.Equal(t, 0, stats.TotalRuns)
	assert.Equal(t, 0, stats.SuccessCount)
	assert.Equal(t, 0, stats.FailureCount)
	assert.Equal(t, 0.0, stats.AvgDurationSecs)
}

// TestInstanceRepo_Stats_CountsByState verifies the aggregates once runs exist:
// completed/failed are tallied, non-terminal states are excluded from the
// success/failure counts, and total_runs counts every instance in the window.
func TestInstanceRepo_Stats_CountsByState(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := localUserID(t)

	seedNamedInstance(t, db, owner, "report", "completed")
	seedNamedInstance(t, db, owner, "report", "completed")
	seedNamedInstance(t, db, owner, "report", "failed")
	seedNamedInstance(t, db, owner, "report", "running")

	from := time.Now().UTC().AddDate(0, 0, -1)
	to := time.Now().UTC().AddDate(0, 0, 1)

	stats, err := repo.Stats(ctx, "report", from, to)
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.Equal(t, 4, stats.TotalRuns)
	assert.Equal(t, 2, stats.SuccessCount)
	assert.Equal(t, 1, stats.FailureCount)
}
