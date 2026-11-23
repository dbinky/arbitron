package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHub_RejectsNilInbox(t *testing.T) {
	p := hubtest.NewPool(t)
	assert.Panics(t, func() {
		runner.NewHub(runner.HubDeps{Registry: p.Registry, Ledger: p.Ledger, Events: p.Events, Health: p.Health, Notifier: p.Notifier, Clock: p.Clock.Now, Config: hubtest.HubConfig()})
	})
}

func TestHubInbox_OutputAckSurvivesNewHub(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, c, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpStream)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	c.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	stream := waitRun(t, ch).out.Stream
	require.NoError(t, h.Handle(ctx, s, outputMsg(a, 1, "durable")))
	assert.Equal(t, "durable", (<-stream).Token)
	ack := c.Await(t, runner.MsgAck, a.Fence.JobID)
	assert.Equal(t, uint64(1), ack.Ack.Seq)
	h.Disconnect(s)
	h2 := p.NewHub(hubtest.HubConfig())
	c2 := hubtest.NewFakeConn()
	_, err := h2.Admit(ctx, key, c2, hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	replay := goRun(ctx, h2, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	got := waitRun(t, replay)
	require.NoError(t, got.err)
	assert.Equal(t, "durable", (<-got.out.Stream).Token)
}

func TestHubInbox_TerminalAckSurvivesNewHub(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, c, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	c.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, okChat("retained result"))))
	require.NoError(t, waitRun(t, ch).err)
	require.True(t, c.Await(t, runner.MsgAck, a.Fence.JobID).Ack.Result)
	h.Disconnect(s)
	out, err := p.NewHub(hubtest.HubConfig()).Run(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	require.NoError(t, err)
	require.NotNil(t, out.Chat)
	assert.Equal(t, "retained result", out.Chat.Output)
}

func TestHubInbox_WriteFailureSendsNoAck(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, c, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	c.Await(t, runner.MsgDispatch, a.Fence.JobID)
	_, err := p.DB.Exec(`CREATE TRIGGER inbox_fail BEFORE INSERT ON runner_inbox BEGIN SELECT RAISE(ABORT,'disk failure'); END`)
	require.NoError(t, err)
	require.Error(t, h.Handle(ctx, s, outputMsg(a, 1, "must persist")))
	require.Error(t, h.Handle(ctx, s, resultMsg(a, okChat("must persist"))))
	assert.Zero(t, c.Count(runner.MsgAck, a.Fence.JobID))
	assertStillRunning(t, ch)
	cancel()
	waitRun(t, ch)
}

func TestHubInbox_ConflictingReplayRejected(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	_, _, _ = admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	repo := adaptsqlite.NewRemoteInboxRepo(p.DB)
	_, err := repo.AcceptOutput(ctx, a.Fence, runner.OutputFrame{Seq: 1, Token: "first"})
	require.NoError(t, err)
	_, err = repo.AcceptOutput(ctx, a.Fence, runner.OutputFrame{Seq: 1, Token: "different"})
	require.Error(t, err)
	require.NoError(t, repo.AcceptResult(ctx, a.Fence, okChat("first")))
	require.Error(t, repo.AcceptResult(ctx, a.Fence, okChat("different")))
	frames, err := repo.Outputs(ctx, a.Fence, 0)
	require.NoError(t, err)
	require.Equal(t, []runner.OutputFrame{{Seq: 1, Token: "first"}}, frames)
	receipt, ok, err := repo.Receipt(ctx, a.Fence)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "first", receipt.Result.Chat.Output)
}

func TestHubInbox_StaleGenerationCannotPersist(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	_, _, _ = admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	f := a.Fence
	f.Generation++
	repo := adaptsqlite.NewRemoteInboxRepo(p.DB)
	_, err := repo.AcceptOutput(ctx, f, runner.OutputFrame{Seq: 1, Token: "stale"})
	require.ErrorIs(t, err, runner.ErrFenced)
	require.ErrorIs(t, repo.AcceptResult(ctx, f, okChat("stale")), runner.ErrFenced)
	var count int
	require.NoError(t, p.DB.QueryRow(`SELECT COUNT(*) FROM runner_inbox`).Scan(&count))
	assert.Zero(t, count)
}
