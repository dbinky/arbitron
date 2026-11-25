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

// Assert the actual Hub.Run/Handle sinks, not just the pure descriptor builder.
func TestHubRun_WorkspaceProcessReachesDispatchAndDurableResult(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello())
	puller, tokens := &wsjPuller{}, &wsjTokens{}
	h.SetWorkspaceJobs(*wsjDeps(puller, tokens))
	run := p.SeedRun(t)
	a := p.ReserveJobFor(t, run, "mac-a", "", runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, RunID: run, Op: runner.OpProcess, Affinity: runner.AffinityWorkspace})
	spec := bundledSpec("/srv/ws/instances/" + run.String())
	ch := goRun(ctx, h, a, runner.StepJob{Op: runner.OpProcess, Process: spec})
	msg := conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NotNil(t, msg.Dispatch)
	require.NotNil(t, msg.Dispatch.Process)
	require.NotNil(t, msg.Dispatch.Workspace)
	assert.Equal(t, spec.Bundle.Hash, msg.Dispatch.Process.BundleHash)
	assert.Equal(t, run, msg.Dispatch.Workspace.RunID)
	assert.Equal(t, a.Fence, puller.seen[0].Fence)
	require.Len(t, tokens.scopes, 1)
	assert.Equal(t, msg.Dispatch.Workspace.Resource, tokens.scopes[0].Resource)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	res := runner.RemoteResult{OK: true, Process: &engine.ProcessResult{Stdout: `{"answer":42}`, ExitCode: 0}}
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, res)))
	got := waitRun(t, ch)
	require.NoError(t, got.err)
	assert.Equal(t, res.Process, got.out.Process)
	assert.Nil(t, got.out.Completion, "the dispatcher, not the hub, owns the receipt")
	ack := conn.Await(t, runner.MsgAck, a.Fence.JobID)
	require.NotNil(t, ack.Ack)
	assert.True(t, ack.Ack.Result)
	retained, found, err := adaptsqlite.NewRemoteInboxRepo(p.DB).Receipt(ctx, a.Fence)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, retained.Result)
	assert.Equal(t, res.Process, retained.Result.Process)
}
