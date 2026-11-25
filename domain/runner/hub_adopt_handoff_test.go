package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deliver through the real orphan-result path after the adopter reads its
// receipt but before it registers the live stream waiter.
type adoptionHandoffInbox struct {
	runner.RemoteInbox
	afterRead func()
}

func (i *adoptionHandoffInbox) Receipt(ctx context.Context, f runner.Fence) (runner.InboxReceipt, bool, error) {
	r, ok, err := i.RemoteInbox.Receipt(ctx, f)
	if fn := i.afterRead; fn != nil {
		i.afterRead = nil
		fn()
	}
	return r, ok, err
}

func TestHubAdopt_ResultDuringStreamHandoffClosesStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := hubtest.NewPool(t)
	key := seedAdmitted(t, p, "mac-1")
	a := p.ReserveJob(t, p.SeedRun(t), "mac-1", runner.MakeSourceID("mac-1", runner.CredAnthropicSubscription, ""), runner.OpStream)
	inbox := &adoptionHandoffInbox{RemoteInbox: adaptsqlite.NewRemoteInboxRepo(p.DB)}
	h := runner.NewHub(runner.HubDeps{Inbox: inbox, Registry: p.Registry, Ledger: p.Ledger, Events: p.Events,
		Health: p.Health, Notifier: p.Notifier, Clock: p.Clock.Now, Config: hubtest.HubConfig()})
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.Held = []runner.HeldJobInfo{{JobID: a.Fence.JobID, Generation: a.Fence.Generation, State: runner.HeldRunning}}
	conn := hubtest.NewFakeConn()
	session, err := h.Admit(ctx, key, conn, hello)
	require.NoError(t, err)
	require.NoError(t, h.Handle(ctx, session, runner.Inbound{Kind: runner.MsgOutput, JobID: a.Fence.JobID, Generation: a.Fence.Generation,
		Output: &runner.OutputFrame{Seq: 1, Token: "once"}}))
	inbox.afterRead = func() {
		require.NoError(t, h.Handle(ctx, session, runner.Inbound{Kind: runner.MsgResult, JobID: a.Fence.JobID, Generation: a.Fence.Generation,
			Result: &runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: "once"}}}))
	}
	out, err := h.Adopt(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	require.NoError(t, err)
	text := ""
	for {
		select {
		case c, ok := <-out.Stream:
			require.True(t, ok, "terminal chunk precedes closure")
			text += c.Token
			if c.Done {
				require.NoError(t, c.Error)
				assert.Equal(t, "once", text)
				assert.Zero(t, conn.Count(runner.MsgDispatch, a.Fence.JobID))
				return
			}
		case <-ctx.Done():
			t.Fatal("durably acknowledged result was lost during adoption handoff")
		}
	}
}
