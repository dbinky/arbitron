package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceHomes_SchemaContract(t *testing.T) {
	db := setupTestDB(t)
	rows, err := db.DB.Query(`SELECT name FROM pragma_table_info('workspace_homes')`)
	require.NoError(t, err)
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		cols[n] = true
	}
	for _, c := range []string{"run_id", "runner_id", "generation", "checkpoint_version", "dirty", "non_portable_reason"} {
		assert.True(t, cols[c], "workspace_homes (041) must have column %s", c)
	}
}

func TestWorkspaceHomeRepo_AcquireHandOffHistory(t *testing.T) {
	ctx := context.Background()
	repo := NewWorkspaceHomeRepo(setupTestDB(t).DB)
	run := uuid.New()

	h, err := repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.Home{RunID: run}, h, "no row = server checkpoint only")

	h, err = repo.AcquireResource(ctx, run, runner.BuiltInRunnerID, 0)
	require.NoError(t, err)
	assert.Equal(t, runner.BuiltInRunnerID, h.RunnerID)
	assert.Equal(t, int64(1), h.Generation)

	same, err := repo.AcquireResource(ctx, run, runner.BuiltInRunnerID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), same.Generation, "re-acquire by the owner does not bump")

	_, err = repo.AcquireResource(ctx, run, "mac-1", 1)
	require.ErrorIs(t, err, runner.ErrHomeOwned, "another runner must hand off, not acquire")

	_, err = repo.HandOff(ctx, run, "mac-1", "should_move", 4096, 0, runner.HomeMoveCause{})
	require.ErrorIs(t, err, runner.ErrHomeGenerationConflict, "stale generation refused")

	require.NoError(t, repo.MarkDirty(ctx, run, true))
	h, err = repo.HandOff(ctx, run, "mac-1", "must_move: source exhausted", 4096, 1, runner.HomeMoveCause{})
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID)
	assert.Equal(t, int64(2), h.Generation)
	assert.False(t, h.Dirty, "the new owner starts from the committed checkpoint")

	require.NoError(t, repo.Release(ctx, run, 2))
	h, err = repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.ServerHome, h.RunnerID)
	assert.Equal(t, int64(3), h.Generation)

	hist, err := repo.History(ctx, run)
	require.NoError(t, err)
	require.Len(t, hist, 3)
	assert.Equal(t, []runner.RunnerID{"", runner.BuiltInRunnerID, "mac-1"}, []runner.RunnerID{hist[0].From, hist[1].From, hist[2].From})
	assert.Equal(t, []runner.RunnerID{runner.BuiltInRunnerID, "mac-1", ""}, []runner.RunnerID{hist[0].To, hist[1].To, hist[2].To})
	assert.Equal(t, "must_move: source exhausted", hist[1].Reason)
	assert.Equal(t, int64(4096), hist[1].Bytes)
	assert.Equal(t, []int64{1, 2, 3}, []int64{hist[0].Generation, hist[1].Generation, hist[2].Generation})
	assert.False(t, hist[1].At.IsZero())
}

func TestWorkspaceHomeRepo_CheckpointDirtyAndListing(t *testing.T) {
	ctx := context.Background()
	repo := NewWorkspaceHomeRepo(setupTestDB(t).DB)
	a, b := uuid.New(), uuid.New()
	require.NoError(t, repo.SetCheckpoint(ctx, a, 3), "upserts a server-only home")
	require.NoError(t, repo.SetCheckpoint(ctx, a, 2), "never lowers")
	h, err := repo.GetHome(ctx, a)
	require.NoError(t, err)
	assert.Equal(t, int64(3), h.CheckpointVersion)
	assert.Equal(t, runner.ServerHome, h.RunnerID)

	_, err = repo.AcquireResource(ctx, a, "mac-1", 0)
	require.NoError(t, err)
	_, err = repo.AcquireResource(ctx, b, "mac-1", 0)
	require.NoError(t, err)
	require.NoError(t, repo.MarkDirty(ctx, b, true))
	list, err := repo.ListByRunner(ctx, "mac-1")
	require.NoError(t, err)
	require.Len(t, list, 2)
	byRun := map[uuid.UUID]runner.Home{list[0].RunID: list[0], list[1].RunID: list[1]}
	assert.True(t, byRun[b].Dirty)
	assert.Equal(t, int64(3), byRun[a].CheckpointVersion)
}

// Two workspace writers cannot overlap: N concurrent handoffs with the same
// expected generation produce exactly one winner (file-backed DB, real tx).
func TestWorkspaceHomeRepo_ConcurrentHandOffSingleWinner(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "homes.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, RunMigrations(db))
	repo := NewWorkspaceHomeRepo(db)
	run := uuid.New()
	_, err = repo.AcquireResource(ctx, run, runner.BuiltInRunnerID, 0)
	require.NoError(t, err)

	const writers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.HandOff(ctx, run, runner.RunnerID(fmt.Sprintf("mac-%d", i)), "should_move", 0, 1, runner.HomeMoveCause{})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, runner.ErrHomeGenerationConflict):
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 1, wins)
	assert.Equal(t, writers-1, conflicts)
	h, err := repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, int64(2), h.Generation)
	hist, err := repo.History(ctx, run)
	require.NoError(t, err)
	assert.Len(t, hist, 2, "exactly one handoff recorded")
}

// A suspect runner keeps ownership: the repo never releases on liveness
// changes (the hand-off policy after loss belongs to P3 T4/T10).
func TestWorkspaceHomeRepo_SuspectHomeKeepsOwnership(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	reg := NewRunnerRegistryRepo(db.DB)
	require.NoError(t, reg.UpsertRunner(ctx, runner.Runner{
		ID: "mac-1", Name: "mac-1", StepLimit: 8,
		Liveness: runner.Liveness("online"), Compat: runner.Compatibility{State: runner.CompatState("compatible")},
		CreatedAt: time.Now().UTC(),
	}))
	repo := NewWorkspaceHomeRepo(db.DB)
	run := uuid.New()
	_, err := repo.AcquireResource(ctx, run, "mac-1", 0)
	require.NoError(t, err)
	require.NoError(t, reg.SetLiveness(ctx, "mac-1", runner.Liveness("suspect"), time.Now().UTC()))

	h, err := repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID)
	assert.Equal(t, int64(1), h.Generation)

	snap, err := reg.Snapshot(ctx)
	require.NoError(t, err)
	require.NotNil(t, snap.Homes, "Snapshot fills Homes")
	assert.Equal(t, h, snap.Homes[run])
}

func TestWorkspaceHomeRepo_SetNonPortableMarksAndClears(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewWorkspaceHomeRepo(db.DB)
	run := uuid.New()
	_, err := repo.AcquireResource(ctx, run, "mac-1", 0)
	require.NoError(t, err)
	require.NoError(t, repo.SetCheckpoint(ctx, run, 3))
	require.NoError(t, repo.MarkDirty(ctx, run, true))
	before, err := repo.GetHome(ctx, run)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, repo.SetNonPortable(ctx, run, runner.NonPortablePromisorReason))
		got, err := repo.GetHome(ctx, run)
		require.NoError(t, err)
		want := before
		want.NonPortableReason = runner.NonPortablePromisorReason
		assert.Equal(t, want, got)
		snap, err := NewRunnerRegistryRepo(db.DB).Snapshot(ctx)
		require.NoError(t, err)
		assert.Equal(t, want, snap.Homes[run])
		list, err := repo.ListByRunner(ctx, "mac-1")
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, want, list[0])
	}
	history, err := repo.History(ctx, run)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.NoError(t, repo.SetNonPortable(ctx, run, ""))
	got, err := repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, before, got)
	absent := uuid.New()
	require.NoError(t, repo.SetNonPortable(ctx, absent, runner.NonPortablePromisorReason))
	got, err = repo.GetHome(ctx, absent)
	require.NoError(t, err)
	assert.Equal(t, runner.Home{RunID: absent, NonPortableReason: runner.NonPortablePromisorReason}, got)
	history, err = repo.History(ctx, absent)
	require.NoError(t, err)
	assert.Empty(t, history)
}

func TestHomeMove_ExactOperationSurvivesHistoryRead(t *testing.T) {
	ctx := context.Background()
	repo := NewWorkspaceHomeRepo(setupTestDB(t).DB)
	run, inv := uuid.New(), uuid.New()
	_, err := repo.AcquireResource(ctx, run, runner.BuiltInRunnerID, 0)
	require.NoError(t, err)
	jobs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i := range jobs {
		gen := int64(i + 1)
		_, err = repo.HandOff(ctx, run, runner.RunnerID(fmt.Sprintf("mac-%d", i)), "retry/parallel", int64(i), gen, runner.HomeMoveCause{TargetJobID: &jobs[i], InvocationID: &inv, NodeID: "loop", OperationID: fmt.Sprintf("op-%d", i), FromGeneration: gen, ToGeneration: gen + 1})
		require.NoError(t, err)
	}
	history, err := repo.History(ctx, run)
	require.NoError(t, err)
	require.Len(t, history, 4)
	assert.Nil(t, history[0].TargetJobID)
	for i := range jobs {
		m := history[i+1]
		assert.Equal(t, &jobs[i], m.TargetJobID)
		assert.Equal(t, &inv, m.InvocationID)
		assert.Equal(t, "loop", m.NodeID)
		assert.Equal(t, fmt.Sprintf("op-%d", i), m.OperationID)
		assert.Equal(t, int64(i+1), m.FromGeneration)
		assert.Equal(t, int64(i+2), m.ToGeneration)
	}
}
