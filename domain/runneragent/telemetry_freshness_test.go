package runneragent_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cachedTelemetry struct{ sample runner.Telemetry }

func (c cachedTelemetry) Sample(context.Context) (runner.Telemetry, error) { return c.sample, nil }

func setCachedTelemetry(t *testing.T, h *harness, sample runner.Telemetry) {
	t.Helper()
	var err error
	h.agent, err = runneragent.New(testConfig(), runneragent.Deps{
		Conn: h.conn, Exec: h.exec, Sources: h.src, Telemetry: cachedTelemetry{sample}, Spool: h.spool,
		Identity: fakeIdentity{key: h.key}, Clock: h.clock, Rand: func() float64 { return 1 },
	})
	require.NoError(t, err)
}

// Feed the runtime agent's heartbeat to the real hub window. This checks the
// sink, not merely the agent's intermediate age field.
func recordHeartbeat(t *testing.T, hub *runner.Hub, session *runner.Session, hb *runneragent.Heartbeat) runner.Telemetry {
	t.Helper()
	require.NoError(t, hub.Handle(context.Background(), session, runner.Inbound{
		Kind: runner.MsgHeartbeat, Heartbeat: &runner.HeartbeatInfo{Telemetry: hb.Telemetry, TelemetryAgeMs: hb.TelemetryAgeMs},
	}))
	window := hub.Window(session.RunnerID)
	require.NotEmpty(t, window)
	return window[len(window)-1]
}

func TestTelemetry_CachedSampleAgeSurvivesHeartbeat(t *testing.T) {
	cpu := 42.0
	h := newHarness(t, 64)
	setCachedTelemetry(t, h, runner.Telemetry{SampledAt: h.clock.Now(), CPUPercent: &cpu})
	p := hubtest.NewPool(t)
	hub := p.NewHub(hubtest.HubConfig())
	session, err := hub.Admit(context.Background(), p.SeedRunner(t, "mac-a", 8), hubtest.NewFakeConn(), hubtest.DarwinHello())
	require.NoError(t, err)
	h.start()
	mark := h.clock.mark()
	s := h.admitted()
	for i := 1; i <= 4; i++ {
		mark = h.clock.awaitAndAdvance(t, mark, 5*time.Second)
		p.Clock.Advance(5 * time.Second)
		hb := s.expect(t, runneragent.KindHeartbeat).Heartbeat
		require.Equal(t, int64(i*5000), hb.TelemetryAgeMs)
		got := recordHeartbeat(t, hub, session, hb)
		if i <= 3 {
			require.NotNil(t, got.CPUPercent)
			assert.Equal(t, cpu, *got.CPUPercent)
			assert.Equal(t, p.Clock.Now().Add(-time.Duration(i)*5*time.Second), got.SampledAt)
		} else {
			assert.Nil(t, got.CPUPercent, "resending a cached sample cannot renew its freshness")
		}
	}
}

func TestHubTelemetry_RemoteClockSkewDoesNotChangeFreshness(t *testing.T) {
	for _, skew := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			cpu := 37.0
			h := newHarness(t, 64)
			setCachedTelemetry(t, h, runner.Telemetry{SampledAt: h.clock.Now(), CPUPercent: &cpu})
			p := hubtest.NewPool(t)
			p.Clock.Advance(skew)
			hub := p.NewHub(hubtest.HubConfig())
			session, err := hub.Admit(context.Background(), p.SeedRunner(t, "mac-a", 8), hubtest.NewFakeConn(), hubtest.DarwinHello())
			require.NoError(t, err)
			h.start()
			mark := h.clock.mark()
			s := h.admitted()
			h.clock.awaitAndAdvance(t, mark, 5*time.Second)
			got := recordHeartbeat(t, hub, session, s.expect(t, runneragent.KindHeartbeat).Heartbeat)
			assert.Equal(t, p.Clock.Now().Add(-5*time.Second), got.SampledAt)
			require.NotNil(t, got.CPUPercent)
			assert.Equal(t, cpu, *got.CPUPercent)
		})
	}
}

func TestTelemetry_MissingSampleTimestampCannotBecomeFreshHeartbeat(t *testing.T) {
	cpu := 42.0
	h := newHarness(t, 64)
	setCachedTelemetry(t, h, runner.Telemetry{CPUPercent: &cpu})
	p := hubtest.NewPool(t)
	hub := p.NewHub(hubtest.HubConfig())
	session, err := hub.Admit(context.Background(), p.SeedRunner(t, "mac-a", 8), hubtest.NewFakeConn(), hubtest.DarwinHello())
	require.NoError(t, err)
	h.start()
	mark := h.clock.mark()
	s := h.admitted()
	h.clock.awaitAndAdvance(t, mark, 5*time.Second)
	got := recordHeartbeat(t, hub, session, s.expect(t, runneragent.KindHeartbeat).Heartbeat)
	assert.Nil(t, got.CPUPercent, "missing producer time must not be re-stamped as fresh evidence")
}
