package runner_test

import (
	"context"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWorkspaceBaselineInitializer_FailsClosed(t *testing.T) {
	f := newWSFixture(t)
	for name, mut := range map[string]func(*runner.BaselineDeps){
		"Checkpoints": func(d *runner.BaselineDeps) { d.Checkpoints = nil },
		"Ledger":      func(d *runner.BaselineDeps) { d.Ledger = nil },
		"Leases":      func(d *runner.BaselineDeps) { d.Leases = nil },
		"Homes":       func(d *runner.BaselineDeps) { d.Homes = nil },
	} {
		t.Run(name, func(t *testing.T) {
			d := f.baselineDeps()
			mut(&d)
			assert.PanicsWithValue(t, "runner: NewWorkspaceBaselineInitializer requires "+name, func() { runner.NewWorkspaceBaselineInitializer(d) })
		})
	}
}

// Happy path: the first preparation snapshots the canonical run directory as
// the committed baseline that every later attempt and runner starts from.
func TestWorkspaceBaseline_FirstPreparationCommitsTheRunDirectory(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"README.md": "hello", "main.go": "package main\n"})
	assert.Equal(t, int64(1), base.Version)
	elsewhere := f.restoreTo(t, base)
	assert.Equal(t, "hello", readIn(t, elsewhere, "README.md"))
	assert.Equal(t, "package main\n", readIn(t, elsewhere, "main.go"))
}

// Edge: a restart with a committed baseline and dirty local files never
// re-snapshots; recovery restores the PRIOR baseline.
func TestWorkspaceBaseline_ExistingCommittedRefIsNeverResnapshotted(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "committed"})
	f.write(t, "a.txt", "interrupted edit")
	require.NoError(t, f.homes.MarkDirty(context.Background(), f.run, true))

	require.NoError(t, f.baseline.EnsureBaseline(context.Background(), f.run, f.dir()))
	assert.Equal(t, base, f.latest(t), "ref and manifest hash unchanged")
	commits, _, _ := f.store.counts()
	assert.Equal(t, 1, commits, "the dirty restart copy was never read as a baseline")
	assert.Equal(t, "committed", readIn(t, f.restoreTo(t, f.latest(t)), "a.txt"))
}

// Failure: an active operation without a baseline fails closed instead of
// snapshotting whatever its interrupted process left behind.
func TestWorkspaceBaseline_ActiveJobWithoutBaselineFailsClosed(t *testing.T) {
	f := newWSFixture(t)
	f.write(t, "a.txt", "half-written by a crashed step")
	key := runner.JobKey{RunID: f.run, InvocationID: uuid.New(), OperationID: "edit", Attempt: 1}
	_, err := f.pool.Ledger.EnsureIntent(context.Background(), key, "edit", f.req())
	require.NoError(t, err)

	err = f.baseline.EnsureBaseline(context.Background(), f.run, f.dir())
	require.ErrorIs(t, err, runner.ErrBaselineBlocked)
	assert.False(t, f.hasLatest(t))
	commits, _, _ := f.store.counts()
	assert.Zero(t, commits)
}

// Edge: concurrent initializers produce one baseline; the loser re-reads it.
func TestWorkspaceBaseline_ConcurrentInitializersKeepOneBaseline(t *testing.T) {
	f := newWSFixture(t)
	f.write(t, "a.txt", "v1")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = f.baseline.EnsureBaseline(context.Background(), f.run, f.dir())
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int64(1), f.latest(t).Version)
}

// Error: only the run's own canonical directory may become its baseline.
func TestWorkspaceBaseline_RejectsANonCanonicalDirectory(t *testing.T) {
	f := newWSFixture(t)
	other := t.TempDir()
	require.Error(t, f.baseline.EnsureBaseline(context.Background(), f.run, other))
	require.Error(t, f.baseline.EnsureBaseline(context.Background(), f.run, "instances/"+f.run.String()), "relative paths are rejected")
	require.Error(t, f.baseline.EnsureBaseline(context.Background(), uuid.Nil, f.dir()))
	assert.False(t, f.hasLatest(t))
}

// RR-08: a promisor miss marks the home non-portable and still lets dispatch proceed.
func TestBaseline_PromisorMissingMarksHomeNonPortableAndDispatches(t *testing.T) {
	f := newWSFixture(t)
	f.write(t, "a.txt", "partial clone")
	f.store.set(func(s *storeSpy) { s.promisor = true })

	require.NoError(t, f.baseline.EnsureBaseline(context.Background(), f.run, f.dir()))
	assert.Equal(t, runner.NonPortablePromisorReason, f.home(t).NonPortableReason)
	assert.False(t, f.hasLatest(t), "no catalog baseline is created for a non-portable run")
}
