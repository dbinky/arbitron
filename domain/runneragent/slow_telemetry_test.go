package runneragent_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/require"
)

type blockedTelemetry struct{ started, canceled chan struct{} }

func (b *blockedTelemetry) Sample(ctx context.Context) (runner.Telemetry, error) {
	close(b.started)
	<-ctx.Done()
	close(b.canceled)
	return runner.Telemetry{}, ctx.Err()
}

func TestAgent_SlowTelemetryKeepsSessionResponsiveAndJoinsOnShutdown(t *testing.T) {
	h := newHarness(t, 64)
	tel := &blockedTelemetry{started: make(chan struct{}), canceled: make(chan struct{})}
	var err error
	h.agent, err = runneragent.New(testConfig(), runneragent.Deps{
		Conn: h.conn, Exec: h.exec, Sources: h.src, Telemetry: tel, Spool: h.spool,
		Identity: fakeIdentity{key: h.key}, Clock: h.clock,
	})
	require.NoError(t, err)
	h.start()
	s := h.session()
	mark := h.clock.mark()
	admit(t, s, h.key.Public().(ed25519.PublicKey))
	mark = h.clock.awaitAndAdvance(t, mark, 5*time.Second)
	select {
	case <-tel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("sampling never started")
	}
	for i := 0; i < 3; i++ {
		mark = h.clock.awaitAndAdvance(t, mark, 5*time.Second)
		hb := s.expect(t, runneragent.KindHeartbeat)
		require.NotNil(t, hb.Heartbeat)
		require.Nil(t, hb.Heartbeat.Telemetry.CPUPercent, "pending sampling is not metric evidence")
	}
	h.cancel()
	select {
	case <-tel.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel telemetry sampling")
	}
}
