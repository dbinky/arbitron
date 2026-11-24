package runner_test

import (
	"context"
	"github.com/google/uuid"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

func confirmJob(t *testing.T, model string) runner.StepJob {
	return runner.StepJob{Op: runner.OpConfirm,
		Binding: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Backend: "claude-code", Model: model,
			ContextWindow: provider.Context1M, Effort: provider.EffortHigh, BaseURL: "https://server.example",
			RevealKey: func(context.Context) (string, error) { t.Error("a confirmation never reveals a key"); return "", nil }},
		Confirmation: &runner.ConfirmationRequest{WorkDir: "/srv/arbitron/workspaces/.runner-confirm"}}
}

func TestHubRun_ConfirmShipsOnlyTheNativeDescriptorAndReplaysEvidence(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(confirmSource(t)))
	a := reserveConfirm(t, p, p.SeedRun(t))
	job := confirmJob(t, "claude-opus-5-5")

	ch := goRun(ctx, h, a, job)
	d := conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NotNil(t, d.Dispatch)
	assert.Equal(t, runner.OpConfirm, d.Dispatch.Op)
	assert.Nil(t, d.Dispatch.Chat)
	assert.Equal(t, &runner.ConfirmationDispatch{Model: "claude-opus-5-5", Backend: "claude-code", Provider: "claude-cli",
		ContextWindow: "1m", Effort: "high"}, d.Dispatch.Confirmation, "no server scratch path, URL or key")
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	exact := runner.ConfirmationResult{RequestedModel: "claude-opus-5-5", ExecutedModel: "claude-opus-5-5", SessionID: "rs"}
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, runner.RemoteResult{OK: true, Confirmation: &exact})))
	r := waitRun(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, &exact, r.out.Confirmation)

	out, err := p.NewHub(hubtest.HubConfig()).Run(ctx, a, job)
	require.NoError(t, err, "the durable inbox receipt replays the evidence on a new hub")
	assert.Equal(t, &exact, out.Confirmation)
}

func TestHubRun_ConfirmRefusesNonSubscriptionAndMalformedJobs(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(confirmSource(t)))
	run := p.SeedRun(t)

	luna := reserveConfirm(t, p, run)
	luna.Job.Group.Kind = runner.CredLunaRoute
	_, err := h.Run(ctx, luna, confirmJob(t, "glm-5.3"))
	assert.ErrorIs(t, err, runner.ErrRemoteCredential)

	bare := reserveConfirm(t, p, run)
	_, err = h.Run(ctx, bare, runner.StepJob{Op: runner.OpConfirm})
	assert.ErrorIs(t, err, runner.ErrRemoteOpUnsupported)
	assert.Equal(t, []string{runner.MsgAdmitted}, conn.Kinds(), "nothing was dispatched")
}

func TestHubAdmit_AcceptsConfirmCapabilityEvidence(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	src := hubtest.AnthropicSource()
	src.Capabilities = []runner.ExecutionCapability{{Op: runner.OpConfirm, Backend: "claude-code", Provider: "claude-cli",
		EndpointFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Families: []modelidentity.Family{"anthropic"},
		Models: []string{"claude-opus-5-5"}, ExecutedModel: true}}
	_, _, _ = admit(t, p, h, "mac-a", hubtest.DarwinHello(src))
	_, sources, err := p.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	require.Len(t, sources, 1)
	require.Len(t, sources[0].Capabilities, 1)
	assert.Equal(t, runner.OpConfirm, sources[0].Capabilities[0].Op)
	key := p.SeedRunner(t, "mac-b", 2)
	src.Capabilities[0].Backend = ""
	_, err = h.Admit(context.Background(), key, hubtest.NewFakeConn(), hubtest.DarwinHello(src))
	assert.ErrorIs(t, err, runner.ErrMalformedMessage)
}
func confirmSource(t *testing.T) runner.SourceReport {
	s := hubtest.AnthropicSource()
	s.Capabilities = []runner.ExecutionCapability{hubtest.Capability(runner.OpConfirm, confirmJob(t, "claude-opus-5-5").Binding, "anthropic")}
	return s
}
func reserveConfirm(t *testing.T, p *hubtest.Pool, run uuid.UUID) runner.Assignment {
	b := confirmJob(t, "claude-opus-5-5").Binding
	fp, err := runner.EndpointFingerprint(b)
	require.NoError(t, err)
	return p.ReserveJobFor(t, run, "mac-a", anthropicSrcA, runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}, RunID: run, Affinity: runner.AffinityNone, Op: runner.OpConfirm, Backend: b.Backend, Provider: b.Provider, EndpointFingerprint: fp, Model: b.Model})
}
