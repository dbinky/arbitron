package backendhealth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// countingRunner wraps a scriptRunner and counts how many subprocess Run calls
// it serves, so a TTL-cache test can assert the probe forks at most once per TTL
// regardless of how many times Backends() is called.
type countingRunner struct {
	mu    sync.Mutex
	calls int
	reply func() (lines []string, waitErr error)
}

func (c *countingRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	lines, waitErr := c.reply()
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range lines {
			ch <- l
		}
	}()
	return ch, func() error { return waitErr }, nil
}

func (c *countingRunner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// readyReplies returns a reply func that alternates version-probe then
// login-status replies for a ready host, so each readiness probe pair succeeds.
func readyReplies() func() ([]string, error) {
	i := 0
	return func() ([]string, error) {
		i++
		if i%2 == 1 {
			return []string{"codex-cli 0.137.0"}, nil // --version
		}
		return []string{"Logged in using ChatGPT"}, nil // login status
	}
}

// TTL CACHE — within the TTL window, repeated Backends() calls must serve the
// CACHED verdict and fork NO new subprocesses. This is the load-bearing fix: the
// unauthenticated GET /system/config route must not fork a codex probe pair per
// request (an un-rate-limited amplification primitive).
func TestChecker_CachesWithinTTL(t *testing.T) {
	r := &countingRunner{reply: readyReplies()}
	now := time.Unix(0, 0)
	clk := func() time.Time { return now }

	c := NewChecker(r, 30*time.Second, clk)

	// First call populates the cache: one readiness probe pair (2 subprocess Runs).
	out := c.Backends(context.Background())
	require.Len(t, out, 1)
	assert.Equal(t, "codex", out[0].Name)
	first := r.count()
	require.Equal(t, 2, first, "first Backends() must run exactly one version+login probe pair")

	// Many more calls within the TTL window: NO new forks.
	for i := 0; i < 50; i++ {
		got := c.Backends(context.Background())
		assert.Equal(t, out, got, "cached verdict must be returned verbatim within the TTL")
	}
	assert.Equal(t, first, r.count(), "no subprocess may fork within the TTL window")
}

// TTL EXPIRY — once the TTL elapses, the next Backends() refreshes (forks a new
// probe pair). This proves the cache is time-bounded, not a one-shot freeze.
func TestChecker_RefreshesAfterTTL(t *testing.T) {
	r := &countingRunner{reply: readyReplies()}
	now := time.Unix(0, 0)
	clk := func() time.Time { return now }

	c := NewChecker(r, 30*time.Second, clk)

	c.Backends(context.Background())
	require.Equal(t, 2, r.count())

	// Advance past the TTL: the next call must refresh.
	now = now.Add(31 * time.Second)
	c.Backends(context.Background())
	assert.Equal(t, 4, r.count(), "a call after the TTL must refresh (one new probe pair)")
}

// CONCURRENCY — concurrent Backends() callers must be safe (the RWMutex guards
// the cache); run under -race to catch a data race on the verdict/expiry.
func TestChecker_ConcurrentCallsAreSafe(t *testing.T) {
	r := &countingRunner{reply: readyReplies()}
	now := time.Unix(0, 0)
	c := NewChecker(r, time.Minute, func() time.Time { return now })

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := c.Backends(context.Background())
			assert.Len(t, out, 1)
		}()
	}
	wg.Wait()
}

// DEFAULTS — a nil clock falls back to time.Now and a non-positive TTL falls back
// to a sane default, so the production composition root can construct with just a
// runner + TTL without wiring a clock.
func TestChecker_NilClockAndZeroTTLDefaults(t *testing.T) {
	r := &countingRunner{reply: readyReplies()}
	c := NewChecker(r, 0, nil)
	out := c.Backends(context.Background())
	require.Len(t, out, 1)
	assert.Equal(t, "codex", out[0].Name)
	assert.Greater(t, c.ttl, time.Duration(0), "a non-positive TTL must fall back to a positive default")
}
