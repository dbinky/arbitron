package provider_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminCtx returns a context carrying an admin identity for tests that need to
// pass the ProfileService admin gate.
func adminCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
}

// minimalCapRules returns a CapabilityRules that accepts any profile with
// backend=claude-code and the default axes. Used by paired-integration tests
// that exercise the ProfileService → ProfileCache → Resolver chain without
// needing the full production ruleset.
func minimalCapRules() provider.CapabilityRules {
	return provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-code": {
			Backend:      "claude-code",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context200K},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortHigh},
		},
	})
}

// Reload: a store mutation is invisible to a resolver until Reload runs, then it
// becomes visible. This is the recache-hook contract the Phase-4 service relies
// on (write -> Reload -> resolvers see the edit).
func TestProfileCache_ReloadReflectsMutation(t *testing.T) {
	store := newFakeProfileStore()
	cache := adapterprovider.NewProfileCache(store)
	require.NoError(t, cache.Load(context.Background()))
	r := adapterprovider.NewDynamicProfileResolver(cache, nil)

	_, err := r.Resolve(context.Background(), "coder")
	require.ErrorIs(t, err, provider.ErrUnknownProfile, "absent before the store mutation")

	// Mutate the store behind the cache; still invisible until Reload.
	store.put(provider.Profile{Alias: "coder", Kind: provider.ProfileKindChat})
	_, err = r.Resolve(context.Background(), "coder")
	require.ErrorIs(t, err, provider.ErrUnknownProfile, "still invisible before Reload")

	require.NoError(t, cache.Reload(context.Background()))
	b, err := r.Resolve(context.Background(), "coder")
	require.NoError(t, err, "visible after Reload")
	assert.Equal(t, provider.ProfileKindChat, b.Kind)
}

// Reload via the resolver: the resolver exposes Reload delegating to the cache,
// for callers that hold only the resolver.
func TestDynamicResolver_ReloadDelegates(t *testing.T) {
	store := newFakeProfileStore()
	cache := adapterprovider.NewProfileCache(store)
	require.NoError(t, cache.Load(context.Background()))
	r := adapterprovider.NewDynamicProfileResolver(cache, nil)

	store.put(provider.Profile{Alias: "p", Kind: provider.ProfileKindAgent})
	require.NoError(t, r.Reload(context.Background()))

	_, err := r.Resolve(context.Background(), "p")
	require.NoError(t, err)
}

// Reload empties the cache when the store is drained (the snapshot is replaced,
// not merged) — a deleted profile disappears after Reload.
func TestProfileCache_ReloadReplacesSnapshot(t *testing.T) {
	store := newFakeProfileStore(provider.Profile{Alias: "gone", Kind: provider.ProfileKindChat})
	cache := adapterprovider.NewProfileCache(store)
	require.NoError(t, cache.Load(context.Background()))
	r := adapterprovider.NewDynamicProfileResolver(cache, nil)

	_, err := r.Resolve(context.Background(), "gone")
	require.NoError(t, err)

	require.NoError(t, store.Delete(context.Background(), "gone"))
	require.NoError(t, cache.Reload(context.Background()))

	_, err = r.Resolve(context.Background(), "gone")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile, "a deleted profile disappears after Reload")
}

// Error: a store failure during Reload returns the error but leaves the previous
// cache snapshot intact (the durable-first invariant: a transient store outage
// must not wipe the in-flight snapshot that is currently serving traffic).
func TestProfileCache_FailedReloadLeavesStateIntact(t *testing.T) {
	store := newFakeProfileStore(provider.Profile{Alias: "before", Kind: provider.ProfileKindChat})
	cache := adapterprovider.NewProfileCache(store)
	require.NoError(t, cache.Load(context.Background()))
	r := adapterprovider.NewDynamicProfileResolver(cache, nil)

	_, err := r.Resolve(context.Background(), "before")
	require.NoError(t, err, "sanity: before is visible after initial Load")

	store.listErr = errors.New("store unavailable")
	require.Error(t, cache.Reload(context.Background()), "Reload must propagate the store error")

	// The snapshot must still contain the pre-failure state.
	_, err = r.Resolve(context.Background(), "before")
	assert.NoError(t, err, "a failed Reload must not wipe the previous snapshot")
}

// Edge (Pass 2): NewProfileCacheFromProfiles takes the last entry on a duplicate
// alias. This is the documented "last entry wins" contract.
func TestNewProfileCacheFromProfiles_DuplicateAlias_LastWins(t *testing.T) {
	c := adapterprovider.NewProfileCacheFromProfiles(
		provider.Profile{Alias: "p", Kind: provider.ProfileKindChat},
		provider.Profile{Alias: "p", Kind: provider.ProfileKindAgent},
	)
	r := adapterprovider.NewDynamicProfileResolver(c, nil)

	b, err := r.Resolve(context.Background(), "p")
	require.NoError(t, err)
	assert.Equal(t, provider.ProfileKindAgent, b.Kind, "last entry must win on duplicate alias")
}

// Paired integration (#17): ProfileService write → ProfileCache Reload → resolver
// visible. The ProfileCache is passed as the Reloader to ProfileService, so a
// service.Create triggers cache.Reload internally, and the resolver reading the
// SAME cache sees the new profile immediately — with no external Reload call.
//
// Also verifies: a persist error in service.Create does NOT call Reload, so the
// resolver's snapshot is unaffected (durable-first invariant from the service side).
func TestPaired_ServiceWrite_RecachesResolver(t *testing.T) {
	ctx := context.Background()
	store := newFakeProfileStore()
	cache := adapterprovider.NewProfileCache(store)
	require.NoError(t, cache.Load(ctx))

	svc := provider.NewProfileService(store, minimalCapRules(), cache)
	r := adapterprovider.NewDynamicProfileResolver(cache, nil)

	// Before the write the alias is unknown.
	_, err := r.Resolve(ctx, "new-profile")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)

	// Service write triggers Reload internally; the resolver sees the new profile.
	_, _, err = svc.Create(adminCtx(), provider.Profile{
		Alias: "new-profile", Kind: provider.ProfileKindAgent, Backend: "claude-code",
	})
	require.NoError(t, err)

	b, err := r.Resolve(ctx, "new-profile")
	require.NoError(t, err, "resolver must see the new profile after the service write recaches")
	assert.Equal(t, provider.ProfileKindAgent, b.Kind)

	// A persist error must NOT recache: seed a duplicate so Create returns
	// ErrProfileExists, then verify the resolver state is unchanged.
	_, _, err = svc.Create(adminCtx(), provider.Profile{
		Alias: "new-profile", Kind: provider.ProfileKindChat, Backend: "claude-code",
	})
	assert.ErrorIs(t, err, provider.ErrProfileExists)
	// The Kind in the resolver must still be Agent (the original write), not Chat.
	b2, err := r.Resolve(ctx, "new-profile")
	require.NoError(t, err)
	assert.Equal(t, provider.ProfileKindAgent, b2.Kind, "a failed write must not change the cached snapshot")
}

// Concurrency: concurrent Resolve reads and Reload writes are race-free (run
// under -race). Resolvers take the read-lock; Reload takes the write-lock.
func TestProfileCache_ConcurrentReadReloadSafe(t *testing.T) {
	store := newFakeProfileStore(provider.Profile{Alias: "p", Kind: provider.ProfileKindChat})
	cache := adapterprovider.NewProfileCache(store)
	require.NoError(t, cache.Load(context.Background()))
	r := adapterprovider.NewDynamicProfileResolver(cache, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = r.Resolve(context.Background(), "p")
			_, _ = r.Kind("p")
		}()
		go func() {
			defer wg.Done()
			_ = cache.Reload(context.Background())
		}()
	}
	wg.Wait()
}
