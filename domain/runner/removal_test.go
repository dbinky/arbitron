package runner_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ engine.WorkspaceRemovalHook = (*runner.RemovalNotifier)(nil)

type recordingCheckpoints struct {
	mu      sync.Mutex
	deleted []uuid.UUID
	err     error
}

func (r *recordingCheckpoints) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.deleted = append(r.deleted, id)
	return nil
}

func newNotifier(t *testing.T, r *copyRig, cps *recordingCheckpoints) *runner.RemovalNotifier {
	t.Helper()
	n, err := runner.NewRemovalNotifier(runner.RemovalDeps{Inventory: r.inv, Homes: r.repo, Checkpoints: cps, Deleter: r.hub, Leases: r.leases})
	require.NoError(t, err)
	return n
}

func TestRemovalNotifier_DeletionReachesARunnerOfflineAtRemovalTime(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	cps := &recordingCheckpoints{}
	n := newNotifier(t, r, cps)
	run := uuid.New()
	h, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	n.WorkspaceRemoved(ctx, run)
	assert.Equal(t, []uuid.UUID{run}, cps.deleted, "the server checkpoints go with the workspace")
	home, err := r.repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.ServerHome, home.RunnerID, "the home is released")
	c, ok := statusOf(t, r, run, "mac-1")
	require.True(t, ok)
	assert.Equal(t, runner.CopyDeletePending, c.Status)
	conn := r.connect(t, "mac-1", runner.HeldCopyInfo{RunID: run, HomeGeneration: h.Generation})
	got := deletesSent(conn)
	require.Len(t, got, 1, "the runner is told on reconnect")
	assert.Equal(t, run, got[0].RunID)
	assert.Equal(t, h.Generation, got[0].HomeGeneration)
	conn = r.connect(t, "mac-1")
	assert.Empty(t, deletesSent(conn))
	_, ok = statusOf(t, r, run, "mac-1")
	assert.False(t, ok, "confirmed deleted once the runner stops reporting it")
}

func TestRemovalNotifier_OnlineHoldersAreToldImmediately(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	n := newNotifier(t, r, &recordingCheckpoints{})
	home := r.connect(t, "mac-2")
	stale := r.connect(t, "mac-1")
	run := uuid.New()
	h1, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	h2, err := r.homes.HandOff(ctx, run, "mac-2", "move", 0, h1.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	before := len(deletesSent(stale))
	n.WorkspaceRemoved(ctx, run)
	got := deletesSent(home)
	require.Len(t, got, 1, "the current home deletes")
	assert.Equal(t, h2.Generation+1, got[0].HomeGeneration, "removal covers every generation, including the home's")
	assert.Len(t, deletesSent(stale), before+1, "a known stale holder is told again")
}

func TestRemoval_ActiveWriterPreventsLocalAndRemoteDelete(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	cps := &recordingCheckpoints{}
	n := newNotifier(t, r, cps)
	conn := r.connect(t, "mac-1")
	run := uuid.New()
	h, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	writer, err := r.leases.Claim(ctx, "run:"+run.String(), "job:test-writer", "mac-1")
	require.NoError(t, err)
	n.WorkspaceRemoved(ctx, run)
	assert.Empty(t, cps.deleted, "no checkpoint deletion while a writer holds the resource")
	assert.Empty(t, deletesSent(conn), "no remote delete while a writer holds the resource")
	home, err := r.repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-1"), home.RunnerID, "the home is not released under an active writer")
	assert.Equal(t, h.Generation, home.Generation)
	c, ok := statusOf(t, r, run, "mac-1")
	require.True(t, ok)
	assert.Equal(t, runner.CopyDeletePending, c.Status, "a durable tombstone records the owed delete")
	conn = r.connect(t, "mac-1", runner.HeldCopyInfo{RunID: run, HomeGeneration: h.Generation})
	assert.Empty(t, deletesSent(conn), "the writer's copy is held on reconnect too")
	require.NoError(t, r.leases.Release(ctx, writer))
	require.NoError(t, n.RetryDeferred(ctx))
	assert.Equal(t, []uuid.UUID{run}, cps.deleted)
	got := deletesSent(conn)
	require.Len(t, got, 1)
	assert.Equal(t, h.Generation+1, got[0].HomeGeneration)
	home, err = r.repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.ServerHome, home.RunnerID)
	_, active, err := r.leases.Active(ctx, "run:"+run.String())
	require.NoError(t, err)
	assert.False(t, active, "the removal claim is released")
	require.NoError(t, n.RetryDeferred(ctx))
	assert.Len(t, cps.deleted, 1, "a completed removal is not repeated")
}

func TestNewRemovalNotifier_RequiresDeps(t *testing.T) {
	_, err := runner.NewRemovalNotifier(runner.RemovalDeps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Leases", "the lease is a required dependency")
}

func TestRemovalNotifier_DeleteFailureRetainsTombstone(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	cps := &recordingCheckpoints{err: errors.New("checkpoint disk unavailable")}
	n := newNotifier(t, r, cps)
	run := uuid.New()
	conn := r.connect(t, "mac-1")
	_, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	n.WorkspaceRemoved(ctx, run)
	assert.Empty(t, cps.deleted)
	assert.Empty(t, deletesSent(conn), "failed checkpoint deletion leaves remote cleanup owed")
	for _, id := range []runner.RunnerID{runner.BuiltInRunnerID, "mac-1"} {
		c, ok := statusOf(t, r, run, id)
		require.True(t, ok)
		assert.Equal(t, runner.CopyDeletePending, c.Status)
	}
	// A fresh notifier retries the durable inventory, not process-local state.
	cps.err = nil
	require.NoError(t, newNotifier(t, r, cps).RetryDeferred(ctx))
	assert.Equal(t, []uuid.UUID{run}, cps.deleted)
	require.Len(t, deletesSent(conn), 1)
	_, ok := statusOf(t, r, run, runner.BuiltInRunnerID)
	assert.False(t, ok)
}
