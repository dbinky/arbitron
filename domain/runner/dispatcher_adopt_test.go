package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A restarted dispatcher consumes the real durable inbox and prepared push,
// without a second execution, and publishes only through validated commit.
func TestDispatch_AdoptedWorkspaceCommitsRetainedPushWithoutRedispatch(t *testing.T) {
	f := newWDFixture(t, "mac-1")
	base := f.seedBaseline(t, map[string]string{"a.txt": "before"})
	ctx, sink, inv := f.stepCtx(t, context.Background(), true)
	id, _ := engine.StepIdentityFromContext(ctx)
	j, err := f.pool.Ledger.EnsureIntent(ctx, runner.JobKey{RunID: f.run, InvocationID: inv.ID, OperationID: id.OperationID, Attempt: id.Attempt}, "edit", f.req())
	require.NoError(t, err)
	a := f.reserveJob(t, j.ID, "mac-1")
	att := f.begin(t, a, moveTo("mac-1", runner.TierShouldMove))
	staged, err := f.stage(ctx, att, map[string]string{"a.txt": "survived restart"})
	require.NoError(t, err)
	pushed, err := f.sync.AcceptPush(ctx, a.Fence, f.run, staged)
	require.NoError(t, err)
	inbox := adaptsqlite.NewRemoteInboxRepo(f.pool.DB)
	require.NoError(t, inbox.AcceptResult(ctx, a.Fence, runner.RemoteResult{OK: true, Process: &engine.ProcessResult{Stdout: "retained output"}}))

	// Reconstruct both sync and hub: no memory-only prepared/result cache survives.
	f.sync = runner.NewWorkspaceSync(f.syncDeps())
	f.d.SetWorkspace(f.sync)
	ad, err := runner.NewJobAdopter(runner.AdoptDeps{Ledger: f.pool.Ledger, Registry: f.pool.Registry,
		Hub: f.pool.NewHub(hubtest.HubConfig()), Leases: f.leases, Stopper: &recordingStopper{},
		Events: f.pool.Events, Status: f.status, Audit: &recAudit{}, Clock: f.pool.Clock.Now})
	require.NoError(t, err)
	f.d.SetAdopter(ad)
	out, err := f.d.Dispatch(ctx, f.req(), *f.job())
	require.NoError(t, err)
	require.NotNil(t, out.Process)
	assert.Equal(t, "retained output", out.Process.Stdout)
	require.NotNil(t, out.Completion)
	require.NotNil(t, out.Completion.Workspace)
	assert.Equal(t, pushed.String(), out.Completion.Workspace.Ref)
	assert.Equal(t, att.Resource, out.Completion.Workspace.Resource)
	assert.Equal(t, base, f.latest(t), "adoption alone cannot publish")
	d := dispatched{out: out, sink: sink, inv: inv}
	assert.Equal(t, runner.JobRunning, f.jobOf(t, d).State)
	require.NoError(t, f.commit(ctx, d, f.run))
	assert.Equal(t, runner.JobCompleted, f.jobOf(t, d).State)
	assert.Equal(t, a.Fence.Generation, f.jobOf(t, d).Generation)
	assert.Equal(t, pushed, f.latest(t))
	assert.Equal(t, "survived restart", readIn(t, f.restoreTo(t, pushed), "a.txt"))
	_, held := f.active(t)
	assert.False(t, held)
	for _, r := range f.runners {
		assert.Zero(t, r.count(), "no runner receives a second execution")
	}
}
