package runneragent_test

import (
	"context"
	"crypto/ed25519"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

type slowSources struct {
	mu       sync.Mutex
	calls    int
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (s *slowSources) Sources(context.Context) []runneragent.Source {
	return []runneragent.Source{{Kind: runner.CredAnthropicSubscription, Observation: &runner.ProbeResult{Auth: runner.AuthOK}, ObservationSeq: 1}}
}
func (s *slowSources) Probe(ctx context.Context) []runneragent.Source { return s.Sources(ctx) }
func (s *slowSources) ProbeSource(ctx context.Context, kind runner.CredentialKind, ref string) (runneragent.Source, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	s.started <- struct{}{}
	// Only the first check can be released; the next remains blocked until shutdown.
	var release <-chan struct{}
	if call == 1 {
		release = s.release
	}
	select {
	case <-release:
		return runneragent.Source{Kind: kind, CredentialRef: ref, Observation: &runner.ProbeResult{Auth: runner.AuthLoggedOut}, ObservationSeq: 2}, nil
	case <-ctx.Done():
		close(s.canceled)
		return runneragent.Source{}, ctx.Err()
	}
}
func TestAgent_SlowProbeDoesNotMakeHealthyRunnerSuspect(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 7
	key := ed25519.NewKeyFromSeed(seed)
	clock := newFakeClock(time.Now())
	conn := newFakeConn()
	src := &slowSources{started: make(chan struct{}, 4), release: make(chan struct{}), canceled: make(chan struct{})}
	a, err := runneragent.New(testConfig(), runneragent.Deps{Conn: conn, Exec: newFakeExec(), Sources: src, Telemetry: &fakeTelemetry{clock: clock}, Spool: runneragent.NewMemorySpool(64), Identity: fakeIdentity{key: key}, Clock: clock, Rand: func() float64 { return 1 }})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var s *fakeSession
	select {
	case s = <-conn.sessions:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never dialed")
	}
	m := clock.mark()
	admit(t, s, key.Public().(ed25519.PublicKey))
	push := func(id string) {
		s.push(runneragent.Inbound{Kind: runneragent.KindProbe, Probe: &runneragent.ProbeRequest{RequestID: id, HealthRevision: 4, SourceKind: runner.CredAnthropicSubscription}})
	}
	const first = "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d"
	push(first)
	select {
	case <-src.started:
	case <-time.After(5 * time.Second):
		t.Fatal("fresh probe never started")
	}
	push("1f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d")
	busy := s.expect(t, runneragent.KindProbeResult)
	assert.Equal(t, "1f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", busy.RequestID)
	require.NotNil(t, busy.ProbeResult)
	assert.Nil(t, busy.ProbeResult.Observation)
	for i := 0; i < 8; i++ {
		m = clock.awaitAndAdvance(t, m, 5*time.Second)
		hb := s.expect(t, runneragent.KindHeartbeat)
		require.NotNil(t, hb.Heartbeat)
	}
	src.mu.Lock()
	assert.Equal(t, 1, src.calls)
	src.mu.Unlock()
	close(src.release)
	reply := s.expect(t, runneragent.KindProbeResult)
	assert.Equal(t, first, reply.RequestID)
	assert.Equal(t, uint64(4), reply.HealthRevision)
	require.NotNil(t, reply.ProbeResult.Observation)
	assert.Equal(t, runner.AuthLoggedOut, reply.ProbeResult.Observation.Auth)
	push("2f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d")
	select {
	case <-src.started:
	case <-time.After(5 * time.Second):
		t.Fatal("next probe never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
	select {
	case <-src.canceled:
	default:
		t.Fatal("shutdown must cancel and join probe")
	}
}
