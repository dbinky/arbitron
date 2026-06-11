package backendhealth

import (
	"context"
	"sync"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/config"
)

// defaultTTL is the cache lifetime applied when a non-positive TTL is supplied.
// The health verdict (codex install/login state) changes rarely, so a 30s window
// is ample; it bounds the probe fork rate to at most one pair per TTL no matter
// how often the (unauthenticated, local) GET /system/config route is polled.
const defaultTTL = 30 * time.Second

// Checker is the shared, request-spanning backend-health component. It probes the
// codex backend AT MOST once per TTL and serves a cached verdict to every caller
// in between, so a frequently-polled read surface (HTTP GET /system/config + the
// MCP system_config tool) cannot fork a codex `--version`/`login status` pair per
// request.
//
// SECURITY/DoS: in the documented local single-user target, GET /system/config is
// UNAUTHENTICATED. Without this cache, any local process could turn one cheap HTTP
// GET into two fork/exec calls plus a network round-trip (codex login status),
// an un-rate-limited amplification primitive. The TTL cache closes that: a cached
// read forks nothing.
//
// ONE Checker is constructed in the composition root and injected into BOTH the
// HTTP SystemHandler and the MCP server, so the cache outlives any single request
// — a cache reconstructed per request would cache nothing.
type Checker struct {
	runner claudecli.CommandRunner
	ttl    time.Duration
	now    func() time.Time

	mu        sync.RWMutex
	verdict   []config.RedactedBackend
	expiresAt time.Time
	populated bool
}

// NewChecker constructs a backend-health Checker. runner is the subprocess seam
// (nil falls back to the shared production os/exec runner via codex.Readiness).
// ttl is the cache lifetime (<=0 uses defaultTTL). now is the clock (nil uses
// time.Now); it is injectable so the TTL behavior is hermetically testable.
func NewChecker(runner claudecli.CommandRunner, ttl time.Duration, now func() time.Time) *Checker {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Checker{runner: runner, ttl: ttl, now: now}
}

// Backends returns the backend-scoped health entries, served from the TTL cache.
// Within the TTL window it forks no subprocess; once the window elapses (or on the
// first call) it refreshes by running the readiness probe and caches the result.
//
// It is BEST-EFFORT, identical to the package-level Backends: a probe that
// genuinely faults drops the entry rather than breaking the whole read.
func (c *Checker) Backends(ctx context.Context) []config.RedactedBackend {
	// Fast path: a valid cached verdict under a read lock (read-heavy workload).
	c.mu.RLock()
	if c.populated && c.now().Before(c.expiresAt) {
		v := c.verdict
		c.mu.RUnlock()
		return v
	}
	c.mu.RUnlock()

	// Slow path: refresh under the write lock. Re-check the expiry after acquiring
	// it so concurrent callers that raced past the read check do not each re-probe.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.populated && c.now().Before(c.expiresAt) {
		return c.verdict
	}
	c.verdict = Backends(ctx, c.runner)
	c.expiresAt = c.now().Add(c.ttl)
	c.populated = true
	return c.verdict
}
