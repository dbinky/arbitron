package runneragent_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

var errSessionClosed = errors.New("fake session closed")

// fakeClock gates time: After channels fire only when Advance passes them.
type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	waiters   []fakeWaiter
	requested []time.Duration
}

func newFakeClock(t0 time.Time) *fakeClock { return &fakeClock{now: t0} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.requested = append(c.requested, d)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		keep = append(keep, w)
	}
	c.waiters = keep
}

// mark returns the number of After calls so far.
func (c *fakeClock) mark() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requested)
}

// awaitAndAdvance waits for an After(d) requested after mark, then advances d.
// It returns the mark to use for the NEXT wait: every After requested after
// this Advance has an index >= the returned value.
func (c *fakeClock) awaitAndAdvance(t *testing.T, mark int, d time.Duration) int {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, r := range c.requested[mark:] {
			if r == d {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond, "no After(%s) requested", d)
	c.mu.Lock()
	next := len(c.requested)
	c.mu.Unlock()
	c.Advance(d)
	return next
}

// requestedSince returns the After durations requested after mark, excluding skip.
func (c *fakeClock) requestedSince(mark int, skip ...time.Duration) []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Duration
	for _, r := range c.requested[mark:] {
		skipped := false
		for _, s := range skip {
			if r == s {
				skipped = true
			}
		}
		if !skipped {
			out = append(out, r)
		}
	}
	return out
}

// fakeSession is one in-memory connection; the test plays the server.
type fakeSession struct {
	toAgent   chan runneragent.Inbound
	fromAgent chan runneragent.Outbound
	closed    chan struct{}
	once      sync.Once
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		toAgent:   make(chan runneragent.Inbound, 64),
		fromAgent: make(chan runneragent.Outbound, 1024),
		closed:    make(chan struct{}),
	}
}

func (s *fakeSession) Send(ctx context.Context, m runneragent.Outbound) error {
	select {
	case <-s.closed:
		return errSessionClosed
	default:
	}
	select {
	case s.fromAgent <- m:
		return nil
	case <-s.closed:
		return errSessionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *fakeSession) Recv(ctx context.Context) (runneragent.Inbound, error) {
	select {
	case m := <-s.toAgent:
		return m, nil
	case <-s.closed:
		return runneragent.Inbound{}, errSessionClosed
	case <-ctx.Done():
		return runneragent.Inbound{}, ctx.Err()
	}
}

func (s *fakeSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeSession) push(in runneragent.Inbound) { s.toAgent <- in }

// expect returns the next message of kind, skipping heartbeats unless asked for.
func (s *fakeSession) expect(t *testing.T, kind string) runneragent.Outbound {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-s.fromAgent:
			if m.Kind == kind {
				return m
			}
			if m.Kind == runneragent.KindHeartbeat {
				continue
			}
			t.Fatalf("expected %q, got %q (%+v)", kind, m.Kind, m)
		case <-deadline:
			t.Fatalf("timed out waiting for %q", kind)
		}
	}
}

// none asserts no message of kind arrives within d.
func (s *fakeSession) none(t *testing.T, kind string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-s.fromAgent:
			if m.Kind == kind {
				t.Fatalf("unexpected %q: %+v", kind, m)
			}
		case <-deadline:
			return
		}
	}
}

// fakeConn hands each dial a fresh fakeSession, after consuming dialErrs.
type fakeConn struct {
	mu       sync.Mutex
	dialErrs []error
	dials    int
	sessions chan *fakeSession
}

func newFakeConn() *fakeConn { return &fakeConn{sessions: make(chan *fakeSession, 16)} }

func (c *fakeConn) Dial(ctx context.Context) (runneragent.Session, error) {
	c.mu.Lock()
	c.dials++
	if len(c.dialErrs) > 0 {
		err := c.dialErrs[0]
		c.dialErrs = c.dialErrs[1:]
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	s := newFakeSession()
	c.sessions <- s
	return s, nil
}

func (c *fakeConn) dialCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dials
}

// fakeExec records every Dispatch it receives and behaves per JobRef.
type execScript func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result

type fakeExec struct {
	mu          sync.Mutex
	calls       []runneragent.Dispatch
	scripts     map[runneragent.JobRef]execScript
	unsupported map[string]string
}

func newFakeExec() *fakeExec {
	return &fakeExec{scripts: map[runneragent.JobRef]execScript{}, unsupported: map[string]string{}}
}

func (e *fakeExec) script(ref runneragent.JobRef, s execScript) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scripts[ref] = s
}

func (e *fakeExec) Supports(d runneragent.Dispatch) (bool, string) {
	if (d.Op != runneragent.OpChat && d.Op != runneragent.OpStream) || d.Chat == nil {
		return false, "unsupported op " + d.Op
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if reason, ok := e.unsupported[d.Chat.CredentialKind]; ok {
		return false, reason
	}
	return true, ""
}

func (e *fakeExec) Execute(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
	e.mu.Lock()
	e.calls = append(e.calls, d)
	s := e.scripts[runneragent.JobRef{JobID: d.JobID, Generation: d.Generation}]
	e.mu.Unlock()
	if s == nil {
		if err := emit(ctx, "ok"); err != nil {
			return runneragent.Result{OK: false, Outcome: runner.SourceOutcome{Class: runner.OutcomeOther}, ErrorText: err.Error()}
		}
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "ok", Model: d.Chat.Model}}
	}
	return s(ctx, d, emit)
}

func (e *fakeExec) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// fakeTelemetry returns a different CPU value per call so a test can prove
// each heartbeat samples afresh; err, when set, is returned instead.
type fakeTelemetry struct {
	mu    sync.Mutex
	calls int
	err   error
	clock *fakeClock
	lag   time.Duration // sample taken this long before the send (cached sample)
}

func (f *fakeTelemetry) Sample(ctx context.Context) (runner.Telemetry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return runner.Telemetry{}, f.err
	}
	cpu := float64(f.calls * 10)
	cores := 10
	return runner.Telemetry{SampledAt: f.clock.Now().Add(-f.lag), CPUPercent: &cpu, Cores: &cores}, nil
}

// fakeSources serves a fixed view and counts probes; Probe flips Detail so
// a refresh is observable.
type fakeSources struct {
	mu     sync.Mutex
	probes int
}

func (f *fakeSources) view() []runneragent.Source {
	return []runneragent.Source{{
		Kind:     runner.CredAnthropicSubscription,
		Families: []modelidentity.Family{"anthropic"},
		Backends: []string{""},
		Health:   runner.SourceHealth{Auth: "ok", Capacity: "available", Detail: "probe-" + itoa(f.probes)},
	}}
}

func (f *fakeSources) Sources(ctx context.Context) []runneragent.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view()
}

func (f *fakeSources) Probe(ctx context.Context) []runneragent.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	return f.view()
}

// ProbeSource counts a fresh check of exactly the requested local source.
func (f *fakeSources) ProbeSource(ctx context.Context, kind runner.CredentialKind, ref string) (runneragent.Source, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind != runner.CredAnthropicSubscription || ref != "" {
		return runneragent.Source{}, runneragent.ErrUnknownSource
	}
	f.probes++
	return f.view()[0], nil
}

func (f *fakeSources) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type fakeIdentity struct{ key ed25519.PrivateKey }

func (f fakeIdentity) Key(context.Context) (ed25519.PrivateKey, error) { return f.key, nil }

// harness runs one Agent against the fakes.
type harness struct {
	t      *testing.T
	agent  *runneragent.Agent
	conn   *fakeConn
	clock  *fakeClock
	exec   *fakeExec
	spool  *runneragent.MemorySpool
	tel    *fakeTelemetry
	src    *fakeSources
	key    ed25519.PrivateKey
	cancel context.CancelFunc
	done   chan error
}

type harnessOpt func(*harness)

func withDialErrors(errs ...error) harnessOpt {
	return func(h *harness) { h.conn.dialErrs = append(h.conn.dialErrs, errs...) }
}

func withSpool(s *runneragent.MemorySpool) harnessOpt { return func(h *harness) { h.spool = s } }

func testConfig() runneragent.Config {
	return runneragent.Config{
		OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "1.4.0",
		HarnessVersions: map[string]string{"claude": "2.1.3"},
		WorkspaceRoot:   "/srv/arbitron/workspaces", ScratchRoot: "/Users/op/.arbitron-runner/scratch",
		Capabilities: []string{runneragent.OpChat},
		Heartbeat:    5 * time.Second, ProbeInterval: time.Hour,
		BackoffBase: 500 * time.Millisecond, BackoffCap: 30 * time.Second,
	}
}

func newHarness(t *testing.T, spoolCap int, opts ...harnessOpt) *harness {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 42
	clock := newFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	h := &harness{
		t: t, conn: newFakeConn(), clock: clock, exec: newFakeExec(),
		spool: runneragent.NewMemorySpool(spoolCap), tel: &fakeTelemetry{clock: clock},
		src: &fakeSources{}, key: ed25519.NewKeyFromSeed(seed), done: make(chan error, 1),
	}
	for _, o := range opts {
		o(h)
	}
	a, err := runneragent.New(testConfig(), runneragent.Deps{
		Conn: h.conn, Exec: h.exec, Sources: h.src, Telemetry: h.tel, Spool: h.spool,
		Identity: fakeIdentity{key: h.key}, Clock: clock, Rand: func() float64 { return 1 },
	})
	require.NoError(t, err)
	h.agent = a
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- h.agent.Run(ctx) }()
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			h.t.Error("agent did not stop")
		}
	})
}

func (h *harness) session() *fakeSession {
	h.t.Helper()
	select {
	case s := <-h.conn.sessions:
		return s
	case <-time.After(5 * time.Second):
		h.t.Fatal("agent never dialed")
		return nil
	}
}

const testKeyID = "6b1f3c1e-5d2a-4c5b-9e0f-7a8b9c0d1e2f"

// admit plays the server side of a successful handshake and returns hello.
func admit(t *testing.T, s *fakeSession, pub ed25519.PublicKey) runneragent.Hello {
	t.Helper()
	nonce := []byte("0123456789abcdef0123456789abcdef")
	s.push(runneragent.Inbound{Kind: runneragent.KindChallenge, Challenge: &runneragent.Challenge{KeyID: testKeyID, Nonce: nonce}})
	cr := s.expect(t, runneragent.KindChallengeResponse)
	require.NotNil(t, cr.ChallengeResponse)
	require.Equal(t, pub, cr.ChallengeResponse.PublicKey)
	require.True(t, ed25519.Verify(pub, runneragent.ChallengeMessage(testKeyID, nonce), cr.ChallengeResponse.Signature))
	hm := s.expect(t, runneragent.KindHello)
	require.NotNil(t, hm.Hello)
	s.push(runneragent.Inbound{Kind: runneragent.KindAdmitted, Admitted: &runneragent.Admission{RunnerID: "mac-studio", HeartbeatSeconds: 5}})
	return *hm.Hello
}

func (h *harness) admitted() *fakeSession {
	s := h.session()
	admit(h.t, s, h.key.Public().(ed25519.PublicKey))
	return s
}

func dispatch(ref runneragent.JobRef, kind string) runneragent.Inbound {
	return runneragent.Inbound{
		Kind: runneragent.KindDispatch, JobID: ref.JobID, Generation: ref.Generation,
		Dispatch: &runneragent.Dispatch{Op: runneragent.OpChat, Chat: &runneragent.ChatJob{
			Model: "claude-sonnet-4-5", Prompt: "hi", CredentialKind: kind,
		}},
	}
}
