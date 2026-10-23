package provider_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

var sonnetTarget = &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}

// fakeSource records every prospective profile and rejects a call whose
// load-bearing fields (kind, backend, target) are absent, so a caller that
// forgets to pass the complete profile fails here instead of passing vacuously.
type fakeSource struct {
	newest      provider.EligibleCandidate
	newestErr   error
	validateErr error
	newestSeen  []provider.Profile
	startSeen   []provider.Profile
}

func (f *fakeSource) check(p provider.Profile) error {
	if p.FollowTarget == nil || p.Kind == "" || provider.EffectiveBackend(p) == "" && p.ConnectionRef == "" {
		return errors.New("fakeSource: incomplete prospective profile")
	}
	return nil
}

func (f *fakeSource) Newest(_ context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	f.newestSeen = append(f.newestSeen, p.Clone())
	if err := f.check(p); err != nil {
		return provider.EligibleCandidate{}, err
	}
	return f.newest.Clone(), f.newestErr
}

var _ provider.EligibleCandidateSource = (*fakeSource)(nil)

func (f *fakeSource) ValidateStart(_ context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	f.startSeen = append(f.startSeen, p.Clone())
	if err := f.check(p); err != nil {
		return provider.EligibleCandidate{}, err
	}
	if p.Model == "" {
		return provider.EligibleCandidate{}, errors.New("fakeSource: missing starting model")
	}
	if f.validateErr != nil {
		return provider.EligibleCandidate{}, f.validateErr
	}
	proof := selectionProof()
	proof.Model = p.Model
	proof.CanonicalID = p.Model
	return proof, nil
}

func (f *fakeSource) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if err := f.check(p); err != nil {
		return nil, err
	}
	return []provider.VersionRequest{provider.VersionRequestFor(p, "")}, nil
}

func (f *fakeSource) RevalidateStart(_ context.Context, p provider.Profile, candidate provider.EligibleCandidate) error {
	if len(f.startSeen) == 0 || !reflect.DeepEqual(p, f.startSeen[len(f.startSeen)-1]) {
		return errors.New("fakeSource: revalidation lost prospective profile")
	}
	proof := selectionProof()
	proof.Model, proof.CanonicalID = p.Model, p.Model
	if !reflect.DeepEqual(candidate, proof) {
		return errors.New("fakeSource: revalidation lost selection proof")
	}
	return nil
}

type fakeVersions struct {
	err  error
	seen []provider.VersionRequest
}

func (f *fakeVersions) SpecificVersion(_ context.Context, r provider.VersionRequest) (string, error) {
	f.seen = append(f.seen, r)
	if f.err != nil {
		return "", f.err
	}
	return r.Model, nil
}

func followingProfile(alias, model string) provider.Profile {
	return provider.Profile{Alias: alias, Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: model,
		SelectionMode: provider.SelectionFollowing, FollowTarget: sonnetTarget}
}

func newFollowingService(store *memProfileStore, v provider.VersionResolver, s provider.EligibleCandidateSource) (*provider.ProfileService, *fakeReloader) {
	rl := &fakeReloader{}
	svc := provider.NewProfileService(store, rules(), rl)
	svc.SetModelSelection(v, s)
	return svc, rl
}

// Happy: a following profile with no model starts on the newest confirmed
// candidate, validated against the complete prospective profile.
func TestFollowingCreationFromCatalog(t *testing.T) {
	store := newMemProfileStore()
	candidate := selectionProof()
	candidate.Model = "claude-sonnet-4-6-20260101"
	src := &fakeSource{newest: candidate}
	svc, rl := newFollowingService(store, &fakeVersions{}, src)

	stored, _, err := svc.Create(admins(), followingProfile("fast", ""))
	require.NoError(t, err)
	persisted, err := store.Get(context.Background(), "fast")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6-20260101", persisted.Model)
	assert.Equal(t, "claude-sonnet-4-6-20260101", stored.Model)
	assert.Equal(t, *sonnetTarget, *persisted.FollowTarget)
	require.Len(t, src.newestSeen, 1)
	assert.Equal(t, "claude-code", src.newestSeen[0].Backend, "candidate source sees the prospective route")
	assert.Equal(t, 1, rl.calls)
}

// Success: an evidenced explicit start keeps its spelling; a pinned profile
// needs route/version validation but no catalog evidence.
func TestFollowingExplicitStartAndPinnedCreation(t *testing.T) {
	store := newMemProfileStore()
	src := &fakeSource{}
	ver := &fakeVersions{}
	svc, _ := newFollowingService(store, ver, src)

	_, _, err := svc.Create(admins(), followingProfile("fast", "claude-sonnet-4-5-20250929"))
	require.NoError(t, err)
	got, _ := store.Get(context.Background(), "fast")
	assert.Equal(t, "claude-sonnet-4-5-20250929", got.Model)
	require.Len(t, ver.seen, 1)
	assert.Equal(t, "claude-sonnet-4-5-20250929", ver.seen[0].Model)
	assert.Equal(t, "claude-code", ver.seen[0].Backend)
	assert.Empty(t, src.newestSeen, "an explicit start does not ask for the newest candidate")
	assert.Len(t, src.startSeen, 1)

	bare := provider.NewProfileService(newMemProfileStore(), rules(), &fakeReloader{})
	bare.SetModelSelection(modelidentity.NewVersionResolver(), &pinnedSource{})
	_, _, err = bare.Create(admins(), provider.Profile{Alias: "pin", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-sonnet-4-6"})
	require.NoError(t, err, "pinned profiles need no catalog evidence")
}

// Failure: invalid target, unauthorized, unavailable candidate, unusable start
// and unfixed version each yield no write and no reload.
func TestFollowingCreationFailuresWriteNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  context.Context
		p    provider.Profile
		src  *fakeSource
		ver  *fakeVersions
		want error
	}{
		"unauthorized":    {users(), followingProfile("a", ""), &fakeSource{}, &fakeVersions{}, provider.ErrForbidden},
		"unauthenticated": {anon(), followingProfile("a", ""), &fakeSource{}, &fakeVersions{}, provider.ErrUnauthenticated},
		"pinned with target": {admins(), func() provider.Profile {
			p := followingProfile("a", "m")
			p.SelectionMode = provider.SelectionPinned
			return p
		}(), &fakeSource{}, &fakeVersions{}, provider.ErrInvalidSelection},
		"bad target slug": {admins(), func() provider.Profile {
			p := followingProfile("a", "")
			p.FollowTarget = &provider.FollowTarget{Provider: "Anthropic Inc", Tier: "sonnet"}
			return p
		}(), &fakeSource{}, &fakeVersions{}, provider.ErrInvalidSelection},
		"no eligible candidate": {admins(), followingProfile("a", ""), &fakeSource{newestErr: provider.ErrNoEligibleCandidate}, &fakeVersions{}, provider.ErrNoEligibleCandidate},
		"empty candidate model": {admins(), followingProfile("a", ""), &fakeSource{}, &fakeVersions{}, provider.ErrNoEligibleCandidate},
		"start not usable":      {admins(), followingProfile("a", "claude-sonnet-4-5-20250929"), &fakeSource{validateErr: errors.New("not usable on route")}, &fakeVersions{}, nil},
		"mutable alias":         {admins(), followingProfile("a", "sonnet"), &fakeSource{}, &fakeVersions{err: provider.ErrSpecificVersionUnavailable}, provider.ErrSpecificVersionUnavailable},
		"axes incompatible": {admins(), func() provider.Profile {
			p := followingProfile("a", "claude-sonnet-4-5-20250929")
			p.Backend = "codex"
			p.ContextWindow = provider.Context1M
			return p
		}(), &fakeSource{}, &fakeVersions{}, provider.ErrAxisNotApplicable},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemProfileStore()
			svc, rl := newFollowingService(store, tc.ver, tc.src)
			_, _, err := svc.Create(tc.ctx, tc.p)
			require.Error(t, err)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			}
			assert.Equal(t, 0, store.writeCount())
			assert.Equal(t, 0, rl.calls)
		})
	}
}

// Error: a missing injected port can never masquerade as an empty catalog or a
// successful validation; source faults propagate unchanged.
func TestFollowingMissingPortsFailClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		ver provider.VersionResolver
		src provider.EligibleCandidateSource
		p   provider.Profile
	}{
		"nil source, no model":   {&fakeVersions{}, nil, followingProfile("a", "")},
		"nil source, explicit":   {&fakeVersions{}, nil, followingProfile("a", "claude-sonnet-4-5-20250929")},
		"nil versions, explicit": {nil, &fakeSource{}, followingProfile("a", "claude-sonnet-4-5-20250929")},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemProfileStore()
			svc, rl := newFollowingService(store, tc.ver, tc.src)
			_, _, err := svc.Create(admins(), tc.p)
			assert.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
			assert.Equal(t, 0, store.writeCount())
			assert.Equal(t, 0, rl.calls)
		})
	}

	boom := errors.New("catalog db down")
	store := newMemProfileStore()
	svc, _ := newFollowingService(store, &fakeVersions{}, &fakeSource{newestErr: boom})
	_, _, err := svc.Create(admins(), followingProfile("a", ""))
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 0, store.writeCount())
}

// Edge: an unchanged following selection does not need discovery (catalog down),
// while a model, target or mode edit is validated on the prospective profile;
// a switch to following keeps the current fixed version.
func TestFollowingPatchValidatesOnlySelectionEdits(t *testing.T) {
	existing := followingProfile("fast", "claude-sonnet-4-5-20250929")
	existing.AllowedTools = []string{"Read"}
	store := newMemProfileStore(existing)
	down := &fakeSource{newestErr: errors.New("catalog down"), validateErr: errors.New("catalog down")}
	ver := &fakeVersions{}
	svc, _ := newFollowingService(store, ver, down)

	tools := []string{"Read"}
	_, _, err := svc.UpdatePatch(admins(), "fast", provider.ProfilePatch{AllowedTools: &tools})
	require.NoError(t, err, "unchanged tools keep the validated selection while discovery is down")
	assert.Empty(t, down.startSeen)

	newModel := "claude-sonnet-4-6-20260101"
	_, _, err = svc.UpdatePatch(admins(), "fast", provider.ProfilePatch{Model: &newModel})
	require.Error(t, err, "a model edit is validated and discovery is down")
	got, _ := store.Get(context.Background(), "fast")
	assert.Equal(t, "claude-sonnet-4-5-20250929", got.Model)

	pinned := provider.Profile{Alias: "pin", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-sonnet-4-5-20250929"}
	store2 := newMemProfileStore(pinned)
	ok := &fakeSource{}
	svc2, _ := newFollowingService(store2, &fakeVersions{}, ok)
	mode := "following"
	var tgt *provider.FollowTarget = sonnetTarget
	_, _, err = svc2.UpdatePatch(admins(), "pin", provider.ProfilePatch{SelectionMode: &mode, FollowTarget: &tgt})
	require.NoError(t, err)
	got, _ = store2.Get(context.Background(), "pin")
	assert.Equal(t, "claude-sonnet-4-5-20250929", got.Model, "switching to following retains the current fixed version")
	require.Len(t, ok.startSeen, 1)
	assert.Empty(t, ok.newestSeen)
}

// ListInternal is the trusted reader: it needs no identity, and an erroring
// store propagates.
func TestProfileServiceListInternal(t *testing.T) {
	store := newMemProfileStore(followingProfile("a", "m1"), provider.Profile{Alias: "b"})
	svc := provider.NewProfileService(store, rules(), &fakeReloader{})
	got, err := svc.ListInternal(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 2)
	_, err = svc.List(context.Background())
	assert.ErrorIs(t, err, provider.ErrUnauthenticated, "the public reader still requires identity")
}

// SeedBatch is the trusted boot path: no identity is needed, a model-less
// following entry starts on the newest candidate through CreateValidated, a
// refused entry persists nothing and satisfies IsInvalidProfile, and a store
// fault does not (the seed must abort on it, not skip).
func TestSeedBatchTrustedPathPreparesLikeAdminCreate(t *testing.T) {
	candidate := selectionProof()
	candidate.Model = "claude-sonnet-4-6-20260101"
	store := newMemProfileStore()
	svc, _ := newFollowingService(store, &fakeVersions{}, &fakeSource{newest: candidate})
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	out, err := seedViaBatch(svc, store, followingProfile("fast", ""))
	require.NoError(t, err)
	assert.Equal(t, candidate.Model, out.Model)
	stored, err := store.Get(context.Background(), "fast")
	require.NoError(t, err)
	assert.Equal(t, candidate.Model, stored.Model)

	// An unproved model-less entry is refused and nothing is stored.
	empty := newMemProfileStore()
	svc, _ = newFollowingService(empty, &fakeVersions{}, &fakeSource{newestErr: provider.ErrNoEligibleCandidate})
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	_, err = seedViaBatch(svc, empty, followingProfile("fast", ""))
	require.ErrorIs(t, err, provider.ErrNoEligibleCandidate)
	assert.True(t, provider.IsInvalidProfile(err))
	_, getErr := empty.Get(context.Background(), "fast")
	assert.ErrorIs(t, getErr, provider.ErrProfileNotFound)

	// A missing selection port is a refusal too, never a vacuous success.
	portless := newMemProfileStore()
	svc = provider.NewProfileService(portless, rules(), &fakeReloader{})
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	_, err = seedViaBatch(svc, portless, followingProfile("fast", ""))
	require.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
	assert.True(t, provider.IsInvalidProfile(err))

	// A model-bearing entry is proved exactly like an admin create: a version
	// refusal or an unconfirmed start is refused and stores nothing.
	versionRefused := newMemProfileStore()
	src := &fakeSource{}
	svc, _ = newFollowingService(versionRefused, &fakeVersions{err: errors.New("mutable alias")}, src)
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	_, err = seedViaBatch(svc, versionRefused, followingProfile("kept", "sonnet"))
	require.Error(t, err)
	assert.True(t, provider.IsInvalidProfile(err))
	assert.Empty(t, src.startSeen, "a refused version never reaches candidate proof")
	_, getErr = versionRefused.Get(context.Background(), "kept")
	assert.ErrorIs(t, getErr, provider.ErrProfileNotFound)

	unproved := newMemProfileStore()
	src = &fakeSource{validateErr: provider.ErrNoEligibleCandidate}
	svc, _ = newFollowingService(unproved, &fakeVersions{}, src)
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	_, err = seedViaBatch(svc, unproved, followingProfile("kept", "claude-sonnet-4-5-20250929"))
	require.ErrorIs(t, err, provider.ErrNoEligibleCandidate)
	assert.True(t, provider.IsInvalidProfile(err))
	require.Len(t, src.startSeen, 1)
	_, getErr = unproved.Get(context.Background(), "kept")
	assert.ErrorIs(t, getErr, provider.ErrProfileNotFound)

	// An evidenced explicit start persists through ValidateStart, RevalidateStart
	// and CreateValidated (the fake source refuses a revalidation without proof).
	proved := newMemProfileStore()
	src = &fakeSource{}
	svc, _ = newFollowingService(proved, &fakeVersions{}, src)
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	out, err = seedViaBatch(svc, proved, followingProfile("kept", "claude-sonnet-4-5-20250929"))
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5-20250929", out.Model)
	require.Len(t, src.startSeen, 1)
	stored, err = proved.Get(context.Background(), "kept")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5-20250929", stored.Model)

	// A store fault is not a profile refusal.
	assert.False(t, provider.IsInvalidProfile(errors.New("store down")))
}
