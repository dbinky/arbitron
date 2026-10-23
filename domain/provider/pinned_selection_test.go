package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

// pinnedSource is a route source for pinned writes: unlike fakeSource it asks
// nothing of a follow target, because a pinned profile has none.
type pinnedSource struct {
	fakeSource
	routes int
}

func (s *pinnedSource) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	s.routes++
	return []provider.VersionRequest{provider.VersionRequestFor(p, "")}, nil
}

func pinnedProfile(alias, model string) provider.Profile {
	return provider.Profile{Alias: alias, Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: model}
}

// Happy + failure: a pinned write is checked against the trigger's explicit
// version grammar before anything persists (D15).
func TestPinnedCreationRequiresExplicitVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		model string
		ok    bool
	}{
		"fixed snapshot": {"claude-sonnet-4-5-20250929", true},
		"mutable alias":  {"sonnet", false},
		"latest keyword": {"latest", false},
		// Trigger refuses an empty model on every route, so a model-less pinned
		// profile would be accepted here and fail every run (paired:12).
		"no model": {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemProfileStore()
			src := &pinnedSource{}
			svc, rl := newFollowingService(store, modelidentity.NewVersionResolver(), src)

			_, _, err := svc.Create(admins(), pinnedProfile("pin", tc.model))
			if tc.ok {
				require.NoError(t, err)
				assert.Equal(t, 2, src.routes, "routes are checked during preparation and again under the mutation lease")
				assert.Equal(t, 1, rl.calls)
				return
			}
			require.Error(t, err)
			assert.True(t, provider.IsInvalidProfile(err))
			assert.Zero(t, store.creates, "a refused pinned profile persists nothing")
			assert.Zero(t, rl.calls)
		})
	}
}

// Error: a half-injected port pair is a wiring fault; the write fails closed
// rather than accepting an unchecked model.
func TestPinnedCreationFailsClosedOnHalfWiredPorts(t *testing.T) {
	for name, tc := range map[string]struct {
		v provider.VersionResolver
		s provider.EligibleCandidateSource
	}{
		"no resolver": {nil, &pinnedSource{}},
		"no source":   {modelidentity.NewVersionResolver(), nil},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemProfileStore()
			svc := provider.NewProfileService(store, rules(), &fakeReloader{})
			svc.SetModelSelection(tc.v, tc.s)
			_, _, err := svc.Create(admins(), pinnedProfile("pin", "claude-sonnet-4-5-20250929"))
			require.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
			assert.Zero(t, store.creates)
		})
	}
}

// Error: a route fault (connection gone, inventory incomplete) refuses the write.
func TestPinnedCreationRouteFaultWritesNothing(t *testing.T) {
	store := newMemProfileStore()
	fault := errors.New("route unavailable")
	svc, _ := newFollowingService(store, modelidentity.NewVersionResolver(), faultySource{fault})
	_, _, err := svc.Create(admins(), pinnedProfile("pin", "claude-sonnet-4-5-20250929"))
	require.ErrorIs(t, err, fault)
	assert.Zero(t, store.creates)
}

type faultySource struct{ err error }

func (f faultySource) Newest(context.Context, provider.Profile) (provider.EligibleCandidate, error) {
	return provider.EligibleCandidate{}, f.err
}
func (f faultySource) ValidateStart(context.Context, provider.Profile) (provider.EligibleCandidate, error) {
	return provider.EligibleCandidate{}, f.err
}
func (f faultySource) VersionRequests(context.Context, provider.Profile) ([]provider.VersionRequest, error) {
	return nil, f.err
}
func (f faultySource) RevalidateStart(context.Context, provider.Profile, provider.EligibleCandidate) error {
	return f.err
}

// Edge: an edit that leaves the selection alone (a resubmit of the stored
// row) needs no fresh route check,
// so a legacy mutable-alias profile stays editable while discovery is down.
func TestPinnedUnchangedSelectionEditSkipsRouteCheck(t *testing.T) {
	legacy := pinnedProfile("pin", "sonnet")
	store := newMemProfileStore(legacy)
	src := &pinnedSource{}
	svc, _ := newFollowingService(store, modelidentity.NewVersionResolver(), src)
	_, _, err := svc.Update(admins(), legacy)
	require.NoError(t, err)
	assert.Zero(t, src.routes)
}

// Seed path: a pinned YAML entry that names a model is held to the same fixed
// version grammar as an admin create, so a mutable alias is refused (and the
// seed skips it) instead of persisting a profile every run would fail on. A
// model-less pinned entry names no version, so trigger would refuse it too.
func TestSeedBatchPinnedRequiresExplicitVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		model string
		ok    bool
	}{
		"fixed snapshot": {"claude-sonnet-4-5-20250929", true},
		"mutable alias":  {"sonnet", false},
		"no model":       {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemProfileStore()
			src := &pinnedSource{}
			svc, _ := newFollowingService(store, modelidentity.NewVersionResolver(), src)
			svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())

			_, err := seedViaBatch(svc, store, pinnedProfile("pin", tc.model))
			if tc.ok {
				require.NoError(t, err)
				_, err = store.Get(context.Background(), "pin")
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, provider.IsInvalidProfile(err), "the seed skips, never aborts, on a refused alias")
			assert.Zero(t, store.creates, "a refused pinned seed persists nothing")
		})
	}
}

// Edit: clearing the model of a pinned profile is a selection change and is
// held to the trigger grammar, so the stored fixed version is kept (paired:12).
func TestPinnedPatchClearingModelIsRefused(t *testing.T) {
	stored := pinnedProfile("pin", "claude-sonnet-4-5-20250929")
	store := newMemProfileStore(stored)
	svc, rl := newFollowingService(store, modelidentity.NewVersionResolver(), &pinnedSource{})
	empty := ""
	_, _, err := svc.UpdatePatch(admins(), "pin", provider.ProfilePatch{Model: &empty})
	require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
	assert.True(t, provider.IsInvalidProfile(err))
	got, err := store.Get(context.Background(), "pin")
	require.NoError(t, err)
	assert.Equal(t, stored.Model, got.Model)
	assert.Zero(t, rl.calls)
}

// Startup seeding never discovers: under WithoutSeedDiscovery a following seed
// is neither prepared (no Newest/ValidateStart) nor refused; it stays pending
// for a later call, while a fixed-model seed is created.
func TestSeedBatchWithoutDiscoveryDefersFollowingSeeds(t *testing.T) {
	store := newMemProfileStore()
	src := &pinnedSource{}
	svc, _ := newFollowingService(store, modelidentity.NewVersionResolver(), src)
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	seeds := &memSeedStore{}
	ctx := provider.WithoutSeedDiscovery(context.Background())

	seeded, skipped, err := svc.SeedBatch(ctx, seeds, []provider.Profile{
		pinnedProfile("pin", "claude-sonnet-4-5-20250929"), followingProfile("follow", ""),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, seeded)
	assert.Zero(t, skipped, "a deferred seed is not a refusal")
	assert.Empty(t, src.newestSeen, "no discovery during startup seeding")
	assert.Empty(t, src.startSeen)
	_, err = store.Get(context.Background(), "follow")
	require.ErrorIs(t, err, provider.ErrProfileNotFound)
	pending, err := seeds.PendingProfileSeed(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "follow", pending[0].Alias)
}

// A pending original that can never be prepared (an empty model frozen before
// fixed versions were required) is repaired by the current, valid config
// candidate for the same alias; the journal then holds the candidate.
func TestSeedBatchRepairsUnpreparablePendingOriginal(t *testing.T) {
	store := newMemProfileStore()
	svc, _ := newFollowingService(store, modelidentity.NewVersionResolver(), &pinnedSource{})
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	seeds := &memSeedStore{}
	ctx := context.Background()

	var refusals []string
	onSkip := func(alias string, _ error) { refusals = append(refusals, alias) }
	seeded, skipped, err := svc.SeedBatch(ctx, seeds, []provider.Profile{pinnedProfile("default-chat", "")}, onSkip)
	require.NoError(t, err)
	assert.Equal(t, 0, seeded)
	assert.Equal(t, 1, skipped)
	assert.Equal(t, []string{"default-chat"}, refusals)

	// The corrected loader now ships a fixed model for the alias.
	seeded, skipped, err = svc.SeedBatch(ctx, seeds, []provider.Profile{pinnedProfile("default-chat", "claude-sonnet-5-5")}, onSkip)
	require.NoError(t, err)
	assert.Equal(t, 1, seeded)
	assert.Zero(t, skipped)
	got, err := store.Get(ctx, "default-chat")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", got.Model)
	pending, err := seeds.PendingProfileSeed(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, pending, "the repaired entry completes")
}

// A valid pending original stays frozen: a later, different config candidate
// for the same alias never replaces it.
func TestSeedBatchKeepsValidPendingOriginalFrozen(t *testing.T) {
	store := newMemProfileStore()
	svc, _ := newFollowingService(store, modelidentity.NewVersionResolver(), &pinnedSource{})
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	seeds := &memSeedStore{}
	ctx := context.Background()

	// Freeze a valid original whose preparation is deferred (following, no
	// discovery), then offer a different pinned candidate for the alias.
	_, _, err := svc.SeedBatch(provider.WithoutSeedDiscovery(ctx), seeds, []provider.Profile{followingProfile("fast", "")}, nil)
	require.NoError(t, err)
	_, _, err = svc.SeedBatch(provider.WithoutSeedDiscovery(ctx), seeds, []provider.Profile{pinnedProfile("fast", "claude-sonnet-5-5")}, nil)
	require.NoError(t, err)
	_, err = store.Get(ctx, "fast")
	require.ErrorIs(t, err, provider.ErrProfileNotFound, "a deferred original is not repaired")
	pending, err := seeds.PendingProfileSeed(ctx, nil)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, provider.SelectionFollowing, pending[0].SelectionMode, "the original stays frozen")

	// A valid pinned original is likewise kept over a different candidate.
	store2 := newMemProfileStore()
	svc2, _ := newFollowingService(store2, modelidentity.NewVersionResolver(), &pinnedSource{})
	svc2.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	seeds2 := &memSeedStore{started: true, pending: []provider.Profile{pinnedProfile("pin", "claude-sonnet-4-5-20250929")}}
	seeded, _, err := svc2.SeedBatch(ctx, seeds2, []provider.Profile{pinnedProfile("pin", "claude-opus-5-5")}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, seeded)
	got, err := store2.Get(ctx, "pin")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5-20250929", got.Model)
}
