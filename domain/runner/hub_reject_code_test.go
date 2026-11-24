package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only the allowlisted typed codes become preparation failures; prose, empty,
// unknown and source-outcome words stay ordinary failures (no reroute loop).
func TestHubRun_RejectCodeMapsOnlyAllowlistedCodes(t *testing.T) {
	for _, tc := range []struct {
		code string
		want runner.OutcomeClass
	}{
		{runner.RejectCommandUnavailable, runner.OutcomePreparationFailed},
		{runner.RejectCapabilityUnavailable, runner.OutcomePreparationFailed},
		{runner.RejectWorkspacePreparationFailed, runner.OutcomePreparationFailed},
		{"", runner.OutcomeOther},
		{"made_up_code", runner.OutcomeOther},
		{"exhausted", runner.OutcomeOther},
	} {
		t.Run("code="+tc.code, func(t *testing.T) {
			p := hubtest.NewPool(t)
			h := p.NewHub(hubtest.HubConfig())
			ctx := context.Background()
			s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
			a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
			ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
			conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
			rej := jobMsg(runner.MsgReject, a)
			rej.RejectReason, rej.RejectCode = "jq not on PATH", tc.code
			require.NoError(t, h.Handle(ctx, s, rej))
			r := waitRun(t, ch)
			assert.ErrorIs(t, r.err, runner.ErrJobRejected)
			assert.Contains(t, r.err.Error(), "jq not on PATH")
			assert.Equal(t, tc.want, runner.ClassifyError(r.err).Class)
			assert.Empty(t, p.Health.Calls(), "a reject never transitions a source")
		})
	}
}

// A runner-reported pull failure keeps its class through the hub.
func TestHubRun_PullFailedResultIsAWorkspaceOutcome(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, runner.RemoteResult{OK: false, ErrorText: "pull failed",
		Outcome: runner.SourceOutcome{Class: runner.OutcomePullFailed}})))
	r := waitRun(t, ch)
	assert.Equal(t, runner.OutcomePullFailed, runner.ClassifyError(r.err).Class)
}
