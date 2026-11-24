// domain/runner/hub_test.go
package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
)

func hubFloat(v float64) *float64 { return &v }

// admit seeds runner id (step limit 8) and admits it on a fresh FakeConn.
func admit(t *testing.T, p *hubtest.Pool, h *runner.Hub, id runner.RunnerID, hello runner.HelloInfo) (*runner.Session, *hubtest.FakeConn, runner.RunnerKey) {
	t.Helper()
	key := p.SeedRunner(t, id, 8)
	conn := hubtest.NewFakeConn()
	s, err := h.Admit(context.Background(), key, conn, hello)
	require.NoError(t, err)
	return s, conn, key
}

func anthropicChat(aff runner.Affinity) runner.ExecRequirements {
	return runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Model: "claude-opus-5-5", Op: runner.OpChat, Affinity: aff, RunID: uuid.New()}
}

func place(t *testing.T, p *hubtest.Pool, req runner.ExecRequirements) runner.Decision {
	t.Helper()
	snap, err := p.Registry.Snapshot(context.Background())
	require.NoError(t, err)
	snap.Now = p.Clock.Now()
	return runner.Place(req, snap, runner.PlacementPolicy{})
}

func heartbeat(sources ...runner.SourceReport) runner.Inbound {
	return runner.Inbound{Kind: runner.MsgHeartbeat, Heartbeat: &runner.HeartbeatInfo{Sources: sources}}
}

func TestHubAdmit_CompatibleRunnerGoesOnlineWithItsSources(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	wake, unsubscribe := p.Notifier.Subscribe()
	defer unsubscribe()

	s, conn, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource(), hubtest.LunaRouteSource("lunaroute", "zhipuai")))
	assert.Equal(t, runner.RunnerID("mac-a"), s.RunnerID)
	assert.Equal(t, key.ID, s.KeyID)
	require.NotEmpty(t, conn.Sent())
	assert.Equal(t, runner.MsgAdmitted, conn.Sent()[0].Kind, "admitted is the first message")

	r, sources, err := p.Registry.GetRunner(ctx, "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.LivenessOnline, r.Liveness)
	require.NotNil(t, r.LastContact)
	assert.True(t, r.LastContact.Equal(p.Clock.Now()))
	assert.Equal(t, runner.CompatCompatible, r.Compat.State)
	assert.Equal(t, "darwin", r.OS)
	assert.Equal(t, "0.2.0", r.RunnerVersion)
	assert.Equal(t, hubtest.ServerRoot, r.WorkspaceRoot)
	ids := []runner.SourceID{}
	for _, src := range sources {
		ids = append(ids, src.ID)
		assert.Equal(t, runner.AuthOK, src.Health.Auth, "reported auth state is recorded")
	}
	assert.ElementsMatch(t, []runner.SourceID{
		runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""),
		runner.MakeSourceID("mac-a", runner.CredLunaRoute, "lunaroute"),
	}, ids)
	assert.Equal(t, []string{"runner.connected"}, p.EventKinds(t))
	select {
	case <-wake:
	default:
		t.Fatal("admission must wake the dispatcher")
	}
	d := place(t, p, anthropicChat(runner.AffinityNone))
	require.True(t, d.Placed, d.Reason)
	assert.Equal(t, runner.RunnerID("mac-a"), d.Runner)
}

func TestHubAdmit_OnlyRemoteCredentialKindsAreRecorded(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	server := runner.SourceReport{Kind: runner.CredServerConnection, CredentialRef: "azure-east", Families: nil}
	local := runner.SourceReport{Kind: runner.CredLocalHarness, CredentialRef: "pi"}
	admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource(), server, local, hubtest.AnthropicSource()))
	_, sources, err := p.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	require.Len(t, sources, 1, "server-connection/local-harness are built-in only; duplicates collapse")
	assert.Equal(t, runner.CredAnthropicSubscription, sources[0].Kind)
}

func TestHubAdmit_IncompatibleRunnerIsVisibleButGetsNoWork(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, _, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	h.Disconnect(s)

	old := hubtest.DarwinHello(hubtest.AnthropicSource())
	old.HarnessVersions = map[string]string{"claude": "2.0.9"}
	conn := hubtest.NewFakeConn()
	_, err := h.Admit(ctx, key, conn, old)
	var inc *runner.IncompatibleError
	require.ErrorAs(t, err, &inc)
	assert.Contains(t, inc.Compat.Reason, "claude")
	assert.Empty(t, conn.Sent(), "an incompatible runner is never admitted")

	r, srcs, err := p.Registry.GetRunner(ctx, "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.CompatIncompatible, r.Compat.State)
	assert.Equal(t, runner.LivenessOffline, r.Liveness)
	assert.Len(t, srcs, 1, "still visible with its sources")
	assert.Equal(t, []string{"runner.connected", "runner.incompatible"}, p.EventKinds(t))
	assert.Equal(t, inc.Compat.Reason, p.EventsOf(t, "runner.incompatible")[0].Detail["reason"])
	assert.False(t, place(t, p, anthropicChat(runner.AffinityNone)).Placed, "no work for an incompatible runner")
}

func TestHubAdmit_NonDarwinAndFutureProtocolRefused(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	key := p.SeedRunner(t, "box", 8)
	linux := hubtest.DarwinHello()
	linux.OS = "linux"
	_, err := h.Admit(context.Background(), key, hubtest.NewFakeConn(), linux)
	var inc *runner.IncompatibleError
	require.ErrorAs(t, err, &inc)
	assert.Contains(t, inc.Compat.Reason, "darwin")

	future := hubtest.DarwinHello()
	future.ProtocolVersion = 2
	_, err = h.Admit(context.Background(), key, hubtest.NewFakeConn(), future)
	require.ErrorAs(t, err, &inc)
	assert.Contains(t, inc.Compat.Reason, "protocol version 2")
}

func TestHubAdmit_WorkspaceRootMismatchIsChatOnly(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.WorkspaceRoot = "/Users/op/.arbitron-runner/workspaces"
	admit(t, p, h, "mac-a", hello)
	r, _, err := p.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.CompatChatOnly, r.Compat.State)
	assert.Contains(t, r.Compat.Reason, "/Users/op/.arbitron-runner/workspaces")
	assert.Equal(t, runner.LivenessOnline, r.Liveness)
	d := place(t, p, anthropicChat(runner.AffinityNone))
	require.True(t, d.Placed, "hermetic chat still runs on a chat_only runner")
	assert.Equal(t, runner.RunnerID("mac-a"), d.Runner)
}

func TestHubAdmit_RevokedKeyRefused(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	key := p.SeedRunner(t, "mac-a", 8)
	now := p.Clock.Now()
	key.RevokedAt = &now
	conn := hubtest.NewFakeConn()
	_, err := h.Admit(context.Background(), key, conn, hubtest.DarwinHello())
	assert.ErrorIs(t, err, runner.ErrRunnerRevoked)
	assert.Empty(t, conn.Sent())
}

func TestHubAdmit_NewConnectionSupersedesOld(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s1, c1, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	c2 := hubtest.NewFakeConn()
	s2, err := h.Admit(ctx, key, c2, hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	code, _, closed := c1.Closed()
	assert.True(t, closed)
	assert.Equal(t, runner.CloseSuperseded, code)
	assert.ErrorIs(t, h.Handle(ctx, s1, heartbeat()), runner.ErrSessionClosed)
	h.Disconnect(s1)
	assert.NoError(t, h.Handle(ctx, s2, heartbeat()), "disconnecting the stale session leaves the new one current")
	assert.Equal(t, []string{"runner.connected", "runner.reconnected"}, p.EventKinds(t))
}

func TestHubHandle_MalformedMessages(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello())
	ctx := context.Background()
	assert.ErrorIs(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgHeartbeat}), runner.ErrMalformedMessage)
	assert.ErrorIs(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgHello, Hello: &runner.HelloInfo{}}), runner.ErrMalformedMessage)
	assert.ErrorIs(t, h.Handle(ctx, nil, heartbeat()), runner.ErrSessionClosed)
}

func TestHubHeartbeat_RecordsBoundedTelemetryAndContact(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	for i := 1; i <= 6; i++ {
		p.Clock.Advance(5 * time.Second)
		require.NoError(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgHeartbeat,
			Heartbeat: &runner.HeartbeatInfo{Telemetry: runner.Telemetry{CPUPercent: hubFloat(float64(i))}}}))
	}
	w := h.Window("mac-a")
	require.Len(t, w, 4, "window is bounded by TelemetryWindow")
	assert.Equal(t, 3.0, *w[0].CPUPercent)
	assert.Equal(t, 6.0, *w[3].CPUPercent)
	assert.True(t, w[3].SampledAt.Equal(p.Clock.Now()), "a zero SampledAt is stamped on receipt")
	assert.Equal(t, 6.0, *h.Latest()["mac-a"].CPUPercent)
	w[0] = runner.Telemetry{}
	assert.Equal(t, 3.0, *h.Window("mac-a")[0].CPUPercent, "Window returns a copy")

	r, _, err := p.Registry.GetRunner(ctx, "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.LivenessOnline, r.Liveness)
	assert.True(t, r.LastContact.Equal(p.Clock.Now()), "every heartbeat refreshes last contact")
}

func TestHub_RecordTelemetryFeedsBuiltIn(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	cores := 12
	h.RecordTelemetry(runner.BuiltInRunnerID, runner.Telemetry{SampledAt: p.Clock.Now(), Cores: &cores})
	got, ok := h.Latest()[runner.BuiltInRunnerID]
	require.True(t, ok)
	assert.Equal(t, 12, *got.Cores)
	assert.Empty(t, h.Window("nobody"))
}

func TestHubHeartbeat_SourceHealthTransitions(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	sid := runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, "")
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.Len(t, p.Health.Observations(), 1, "the hello observation is merged once")
	beat := func(hl runner.SourceHealth, obs *runner.ProbeResult, seq uint64) {
		t.Helper()
		src := hubtest.AnthropicSource()
		src.Health, src.Observation, src.ObservationSeq = hl, obs, seq
		require.NoError(t, h.Handle(ctx, s, heartbeat(src)))
	}

	beat(runner.SourceHealth{Auth: runner.AuthLoggedOut, Capacity: runner.CapacityExhausted}, nil, 0)
	assert.Len(t, p.Health.Observations(), 1, "a projection without an observation never changes server health")
	assert.Empty(t, p.Health.Calls())

	beat(runner.SourceHealth{}, &runner.ProbeResult{Auth: runner.AuthLoggedOut}, 2)
	beat(runner.SourceHealth{}, &runner.ProbeResult{Auth: runner.AuthLoggedOut}, 2)
	obs := p.Health.Observations()
	require.Len(t, obs, 2, "a repeated observation sequence is applied once")
	assert.Equal(t, sid, obs[1].Source)
	assert.Equal(t, runner.AuthLoggedOut, obs[1].Result.Auth)
	src, err := p.Registry.GetSource(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, src.Health.Revision, obs[1].Result.HealthRevision, "stamped with the current revision")
	assert.False(t, place(t, p, anthropicChat(runner.AffinityNone)).Placed, "logged-out source gets no work")

	held, reset := false, p.Clock.Now().Add(3*time.Hour)
	beat(runner.SourceHealth{}, &runner.ProbeResult{Auth: runner.AuthOK, Capacity: &runner.CapacityObservation{
		State: runner.CapacityExhausted, OrdinaryUsageAllowed: &held, ResetAt: &reset}}, 3)
	obs = p.Health.Observations()
	require.Len(t, obs, 3)
	require.NotNil(t, obs[2].Result.Capacity, "exhaustion hints pass through")

	allowed := true
	beat(runner.SourceHealth{}, &runner.ProbeResult{Auth: runner.AuthOK, Capacity: &runner.CapacityObservation{OrdinaryUsageAllowed: &allowed}}, 4)
	obs = p.Health.Observations()
	require.Len(t, obs, 4)
	assert.Nil(t, obs[3].Result.Capacity, "an uncorrelated heartbeat can never recover capacity")
	beat(runner.SourceHealth{}, &runner.ProbeResult{Auth: runner.AuthOK}, 3)
	assert.Len(t, p.Health.Observations(), 4, "an older sequence is ignored")
}

func TestHubHeartbeat_SourceSetChangeReplacesSources(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	wake, unsubscribe := p.Notifier.Subscribe()
	defer unsubscribe()

	require.NoError(t, h.Handle(ctx, s, heartbeat(hubtest.AnthropicSource(), hubtest.LunaRouteSource("lunaroute", "zhipuai"))))
	_, srcs, err := p.Registry.GetRunner(ctx, "mac-a")
	require.NoError(t, err)
	assert.Len(t, srcs, 2)
	select {
	case <-wake:
	default:
		t.Fatal("a source-set change wakes the dispatcher")
	}

	require.NoError(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgHeartbeat, Heartbeat: &runner.HeartbeatInfo{}}))
	_, srcs, err = p.Registry.GetRunner(ctx, "mac-a")
	require.NoError(t, err)
	assert.Len(t, srcs, 2, "nil Sources means unchanged")
}

func TestNewHub_RejectsMissingDepsAndBadConfig(t *testing.T) {
	p := hubtest.NewPool(t)
	assert.Panics(t, func() { runner.NewHub(runner.HubDeps{Config: hubtest.HubConfig()}) })
	bad := hubtest.HubConfig()
	bad.LostAfter = bad.SuspectAfter
	assert.Panics(t, func() { p.NewHub(bad) })
	bad = hubtest.HubConfig()
	bad.TelemetryWindow = 0
	assert.Panics(t, func() { p.NewHub(bad) })
}
