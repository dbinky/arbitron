package main

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Seed ↔ startup (paired:9). Startup never discovers: following seeds stay
// pending through readiness, the background loop's immediate first cycle runs,
// and the seed retry after it prepares and stores both aliases from that
// cycle's durable catalog and proof. Each catalog fetch records how many
// profiles existed at that moment.
func TestFollowingSeedThenStartupRefreshEachCommitRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}}
	type fetchState struct{ profiles int }
	var mu sync.Mutex
	var ds *domainStack
	var seen []fetchState
	transport := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		stack := ds
		mu.Unlock()
		if stack != nil {
			var s fetchState
			assert.NoError(t, stack.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&s.profiles))
			mu.Lock()
			seen = append(seen, s)
			mu.Unlock()
		}
		return leaf.fetch(r)
	})
	built, ticks, intervals := buildLifecycleRootWithDependencies(t, lifecycleFollowing, leaf, ModelDependencies{FetchTransport: transport})
	ctx := lifecycleConnection(t, built)
	mu.Lock()
	ds = built
	mu.Unlock()

	require.NoError(t, built.Start(ctx))

	mu.Lock()
	boot := append([]fetchState(nil), seen...)
	mu.Unlock()
	require.Len(t, boot, 1, "the first background cycle fetches; the seed retry reuses its durable catalog and proof")
	assert.Equal(t, fetchState{profiles: 0}, boot[0], "startup stored no following alias (no discovery before readiness)")
	for _, alias := range []string{"worker", "other"} {
		p, err := built.profileSvc.Get(ctx, alias)
		require.NoError(t, err, "the seed retry after the first cycle stores %s", alias)
		assert.Equal(t, rootNew, p.Model)
	}
	var reports, pending int
	require.NoError(t, built.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(publication_pending),0) FROM model_refresh_reports`).Scan(&reports, &pending))
	assert.Positive(t, reports, "the first cycle commits its report")
	assert.Zero(t, pending, "no cycle leaves publication pending")
	lifecycleExecute(t, built, ctx, rootNew)

	rootTicker(t, intervals)
	rootTick(t, ticks)
	rootReports(t, built, reports+1)
	mu.Lock()
	assert.Len(t, seen, 2, "the first tick runs one cycle; seeds are complete so no retry discovers again")
	mu.Unlock()
	var pendingSeeds int
	require.NoError(t, built.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&pendingSeeds))
	assert.Zero(t, pendingSeeds)
	lifecycleExecute(t, built, ctx, rootNew)
}

// A boot with no YAML seed has no prospective discovery to stand in for the
// startup pass: Start alone must fetch and commit one cycle (G32).
func TestStartupRefreshCommitsWithoutSeedRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)

	require.NoError(t, ds.Start(ctx))

	leaf.mu.Lock()
	fetches := leaf.fetches
	leaf.mu.Unlock()
	assert.Equal(t, 1, fetches, "the startup pass fetched the catalog")
	view, err := ds.catalogStore.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Len(t, view.Reports, 1, "the startup pass committed its report")
	assert.NotEmpty(t, view.Evidence, "the startup pass probed the configured path")
	rootTicker(t, intervals)
}
