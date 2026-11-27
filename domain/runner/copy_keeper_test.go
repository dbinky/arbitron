package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type copyRig struct {
	pool   *hubtest.Pool
	hub    *runner.Hub
	leases *adaptsqlite.ResourceLeaseRepo
	inv    *adaptsqlite.RunnerCopyRepo
	repo   *adaptsqlite.WorkspaceHomeRepo
	homes  runner.HomeStore
	keeper *runner.CopyKeeper
	keys   map[runner.RunnerID]runner.RunnerKey
}

func newCopyRig(t *testing.T) *copyRig {
	t.Helper()
	p := hubtest.NewPool(t)
	r := &copyRig{pool: p, hub: p.NewHub(hubtest.HubConfig()), leases: adaptsqlite.NewResourceLeaseRepo(p.DB, "boot-test"), inv: adaptsqlite.NewRunnerCopyRepo(p.DB), repo: adaptsqlite.NewWorkspaceHomeRepo(p.DB), keys: map[runner.RunnerID]runner.RunnerKey{}}
	for _, id := range []runner.RunnerID{"mac-1", "mac-2"} {
		r.keys[id] = p.SeedRunner(t, id, 2)
	}
	k, err := runner.NewCopyKeeper(runner.CopyKeeperDeps{Inventory: r.inv, Homes: r.repo, Deleter: r.hub, Leases: r.leases})
	require.NoError(t, err)
	r.keeper = k
	r.hub.SetCopyReconciler(k)
	r.homes = runner.NewCopyTrackingHomes(r.repo, k)
	return r
}

func (r *copyRig) connect(t *testing.T, id runner.RunnerID, copies ...runner.HeldCopyInfo) *hubtest.FakeConn {
	t.Helper()
	conn := hubtest.NewFakeConn()
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.Copies = copies
	_, err := r.hub.Admit(context.Background(), r.keys[id], conn, hello)
	require.NoError(t, err)
	return conn
}

func deletesSent(conn *hubtest.FakeConn) []runner.DeleteCopyPayload {
	var out []runner.DeleteCopyPayload
	for _, m := range conn.Sent() {
		if m.Kind == runner.MsgDeleteCopy && m.DeleteCopy != nil {
			out = append(out, *m.DeleteCopy)
		}
	}
	return out
}

func statusOf(t *testing.T, r *copyRig, run uuid.UUID, id runner.RunnerID) (runner.RunnerCopy, bool) {
	t.Helper()
	all, err := r.inv.ListByRun(context.Background(), run)
	require.NoError(t, err)
	for _, c := range all {
		if c.RunnerID == id {
			return c, true
		}
	}
	return runner.RunnerCopy{}, false
}

func TestCopyKeeper_PlannedMoveTellsOldHomeToDeleteAfterTheNewGeneration(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	old := r.connect(t, "mac-1")
	run := uuid.New()
	h1, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	assert.Empty(t, deletesSent(old), "taking a home deletes nothing")
	h2, err := r.homes.HandOff(ctx, run, "mac-2", "move: load", 1024, h1.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	got := deletesSent(old)
	require.Len(t, got, 1)
	assert.Equal(t, run, got[0].RunID)
	assert.Equal(t, h1.Generation, got[0].HomeGeneration, "only copies at or below the old generation may go")
	assert.Equal(t, h2.Generation-1, got[0].HomeGeneration)
	c, ok := statusOf(t, r, run, "mac-1")
	require.True(t, ok)
	assert.Equal(t, runner.CopyDeletePending, c.Status)
	c, ok = statusOf(t, r, run, "mac-2")
	require.True(t, ok)
	assert.Equal(t, runner.CopyActive, c.Status)
	assert.Equal(t, h2.Generation, c.HomeGeneration)
}

func TestCopyKeeper_ReconnectingStaleCopyIsFencedThenConfirmedDeleted(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	run := uuid.New()
	h1, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	_, err = r.homes.HandOff(ctx, run, "mac-2", "lost", 0, h1.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	conn := r.connect(t, "mac-1", runner.HeldCopyInfo{RunID: run, HomeGeneration: h1.Generation})
	got := deletesSent(conn)
	require.Len(t, got, 1, "a stale generation is told to delete on hello")
	assert.Equal(t, h1.Generation, got[0].HomeGeneration)
	conn = r.connect(t, "mac-1")
	assert.Empty(t, deletesSent(conn))
	_, ok := statusOf(t, r, run, "mac-1")
	assert.False(t, ok, "absence on a later hello confirms the deletion")
}

func TestCopyKeeper_CurrentHomeCopyIsKept(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	run := uuid.New()
	h, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	conn := r.connect(t, "mac-1", runner.HeldCopyInfo{RunID: run, HomeGeneration: h.Generation})
	assert.Empty(t, deletesSent(conn))
	c, ok := statusOf(t, r, run, "mac-1")
	require.True(t, ok)
	assert.Equal(t, runner.CopyActive, c.Status)
}

func TestCopyKeeper_UnknownCopyIsStale(t *testing.T) {
	r := newCopyRig(t)
	run := uuid.New()
	conn := r.connect(t, "mac-2", runner.HeldCopyInfo{RunID: run, HomeGeneration: 4})
	got := deletesSent(conn)
	require.Len(t, got, 1)
	assert.Equal(t, int64(4), got[0].HomeGeneration)
	c, _ := statusOf(t, r, run, "mac-2")
	assert.Equal(t, runner.CopyStale, c.Status)
}

func TestCopyKeeper_StaleDeleteCannotRemoveNewGeneration(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	run := uuid.New()
	h1, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	h2, err := r.homes.HandOff(ctx, run, "mac-2", "move: load", 0, h1.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	h3, err := r.homes.HandOff(ctx, run, "mac-1", "move: back", 0, h2.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	conn := r.connect(t, "mac-1", runner.HeldCopyInfo{RunID: run, HomeGeneration: h3.Generation})
	for _, d := range deletesSent(conn) {
		assert.Less(t, d.HomeGeneration, h3.Generation, "no delete ever names the current generation")
	}
	assert.Empty(t, deletesSent(conn), "the old tombstone never deletes the newer home copy")
	c, ok := statusOf(t, r, run, "mac-1")
	require.True(t, ok)
	assert.Equal(t, runner.CopyActive, c.Status)
	assert.Equal(t, h3.Generation, c.HomeGeneration)
	require.NoError(t, r.inv.MarkForDeletion(ctx, run, "mac-1", h1.Generation, runner.CopyDeletePending))
	c, _ = statusOf(t, r, run, "mac-1")
	assert.Equal(t, runner.CopyActive, c.Status, "a late stale mark cannot downgrade the newer active copy")
}

func TestCopyKeeper_DeleteHeldWhileTheTargetHoldsTheResource(t *testing.T) {
	ctx := context.Background()
	r := newCopyRig(t)
	run := uuid.New()
	h1, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	_, err = r.homes.HandOff(ctx, run, "mac-2", "move: load", 0, h1.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	writer, err := r.leases.Claim(ctx, "run:"+run.String(), "drain:test", "mac-1")
	require.NoError(t, err)
	conn := r.connect(t, "mac-1", runner.HeldCopyInfo{RunID: run, HomeGeneration: h1.Generation})
	assert.Empty(t, deletesSent(conn), "the claim owner's copy is never deleted under it")
	c, ok := statusOf(t, r, run, "mac-1")
	require.True(t, ok)
	assert.Equal(t, runner.CopyDeletePending, c.Status, "the tombstone stays owed")
	_, active, err := r.leases.Active(ctx, "run:"+run.String())
	require.NoError(t, err)
	assert.True(t, active, "the keeper never releases a claim it does not hold")
	require.NoError(t, r.leases.Release(ctx, writer))
	conn = r.connect(t, "mac-1", runner.HeldCopyInfo{RunID: run, HomeGeneration: h1.Generation})
	got := deletesSent(conn)
	require.Len(t, got, 1)
	assert.Equal(t, h1.Generation, got[0].HomeGeneration)
	_, active, err = r.leases.Active(ctx, "run:"+run.String())
	require.NoError(t, err)
	assert.False(t, active, "the delete's own claim is released after the send")
}
