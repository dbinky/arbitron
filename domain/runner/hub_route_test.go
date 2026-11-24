package runner_test

import (
	"context"
	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestHubRun_DispatchCarriesExactRoute(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	job := hubtest.ChatJob("claude-opus-5-5", runner.OpChat)
	_ = goRun(ctx, h, a, job)
	d := conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	want, err := runner.EndpointFingerprint(job.Binding)
	require.NoError(t, err)
	assert.Equal(t, provider.Canonicalize(job.Binding.Provider), d.Dispatch.Chat.Provider)
	assert.Equal(t, want, d.Dispatch.Chat.EndpointFingerprint)
}

func TestHubHeartbeat_TelemetryAgeNormalizesToReceiptTime(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	cpu := 42.0
	skewed := p.Clock.Now().Add(10 * time.Minute) // remote wall clock far ahead
	hb := heartbeat()
	hb.Heartbeat.Telemetry = runner.Telemetry{SampledAt: skewed, CPUPercent: &cpu}
	hb.Heartbeat.TelemetryAgeMs = 2000
	require.NoError(t, h.Handle(ctx, s, hb))
	got := h.Latest()["mac-a"]
	assert.Equal(t, p.Clock.Now().Add(-2*time.Second), got.SampledAt, "receipt minus age, never the runner's clock")
	require.NotNil(t, got.CPUPercent)

	hb.Heartbeat.TelemetryAgeMs = -1
	require.NoError(t, h.Handle(ctx, s, hb))
	got = h.Latest()["mac-a"]
	assert.Nil(t, got.CPUPercent, "an impossible age discards metric evidence")
}
