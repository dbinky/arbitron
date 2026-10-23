package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startWithin runs start and fails the test unless it returns within d.
func startWithin(t *testing.T, d time.Duration, start func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- start() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(d):
		t.Fatalf("start did not return within %s: discovery is gating readiness", d)
	}
}

// Serve readiness never waits on discovery. A usability probe that blocks until
// the stack closes must not hold Start: crash recovery completes and the HTTP
// listener answers while the probe is still blocked, and the background loop's
// immediate first pass really did reach the probe.
func TestServeStartupDoesNotWaitForDiscoveryRoot(t *testing.T) {
	probeEntered := make(chan struct{})
	var probeOnce sync.Once
	leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}}
	blockingProbe := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		probeOnce.Do(func() { close(probeEntered) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ds, _, intervals := buildLifecycleRootWithDependencies(t, `profiles:
  - alias: worker
    kind: chat
    connectionref: root
    model: `+rootOld+`
`, leaf, ModelDependencies{ProviderTransport: blockingProbe})
	ds.awaitInitialDiscovery = false // the production policy
	ctx := lifecycleConnection(t, ds)
	crashed := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: crashed, OwnerID: localDefaultUser, WorkflowName: "crashed", State: instance.StateRunning,
		Priority: instance.PriorityNormal, WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
	}))

	startWithin(t, 10*time.Second, func() error { return ds.Start(ctx) })

	select {
	case <-probeEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("the background loop's immediate first pass never reached a probe")
	}
	select {
	case d := <-intervals:
		assert.Equal(t, 4*time.Hour, d, "serve owns the scheduled loop")
	case <-time.After(2 * time.Second):
		t.Fatal("serve registered no refresh ticker")
	}
	// The probe is still blocked here.
	got, err := ds.instanceRepo.Get(ctx, crashed)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, got.State, "crash recovery completed before readiness")
	p, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, rootOld, p.Model, "the persisted working model set serves meanwhile")
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the listener answers while discovery is blocked")
}

// countingRunner counts harness launches (subscription/agent probes).
type countingRunner struct{ calls atomic.Int32 }

func (r *countingRunner) Run(ctx context.Context, _ claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls.Add(1)
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

// The stdio MCP entrypoint fetches and probes nothing before its handshake and
// registers no refresh loop, on a fresh database and on an existing one with a
// pending following seed (whose preparation would discover). The seed stays
// pending for serve.
func TestMCPStartupFetchesAndProbesNothingRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0o700))
	configPath := filepath.Join(root, "config.yaml")
	cfg := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "arbitron.db") + "\nworkspace:\n  path: " + ws +
		"\nsamples:\n  seed: false\nlogging:\n  level: warn\n  file: " + filepath.Join(root, "arbitron.log") + "\n" + `profiles:
  - alias: fixed
    kind: chat
    model: claude-sonnet-4-5
  - alias: worker
    kind: chat
    selection_mode: following
    follow_target:
      provider: anthropic
      tier: sonnet
`
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	var fetches, providerCalls, tickers atomic.Int32
	blocking := func(counter *atomic.Int32) http.RoundTripper {
		return catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
			counter.Add(1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
	}
	runner := &countingRunner{}
	deps := ModelDependencies{
		FetchTransport: blocking(&fetches), ProviderTransport: blocking(&providerCalls), Runner: runner,
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			tickers.Add(1)
			return make(chan time.Time), func() {}
		},
	}
	for _, boot := range []string{"fresh database", "existing database with a pending seed"} {
		t.Run(boot, func(t *testing.T) {
			var ds *domainStack
			startWithin(t, 10*time.Second, func() error {
				var err error
				ds, err = startMCPStack(context.Background(), configPath, deps)
				return err
			})
			ctx := adminCtxForLocalUser(t, ds)
			fixed, err := ds.profileSvc.Get(ctx, "fixed")
			require.NoError(t, err, "a fixed-model seed needs no discovery and is seeded")
			assert.Equal(t, "claude-sonnet-4-5", fixed.Model)
			_, err = ds.profileSvc.Get(ctx, "worker")
			require.ErrorIs(t, err, provider.ErrProfileNotFound, "a following seed stays pending for serve")
			var pending int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending WHERE alias = 'worker'`).Scan(&pending))
			assert.Equal(t, 1, pending)
			// Give any stray background work a chance to show itself.
			time.Sleep(200 * time.Millisecond)
			assert.Zero(t, fetches.Load(), "no catalog fetch before or after the handshake")
			assert.Zero(t, providerCalls.Load(), "no HTTP usability probe")
			assert.Zero(t, runner.calls.Load(), "no harness usability probe")
			assert.Zero(t, tickers.Load(), "no automatic refresh loop is registered")
			ds.Close(context.Background())
		})
	}
}
