package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lrPolicySource struct {
	EligibleCandidateSource
	provider string
	mixed    bool
	reverse  bool
	err      error
	calls    int
	changeAt int
	switchAt int
}

func (s *lrPolicySource) VersionRequests(_ context.Context, p Profile) ([]VersionRequest, error) {
	s.calls++
	if s.switchAt > 0 && s.calls >= s.switchAt {
		s.provider = "lunaroute"
	}
	if s.err != nil && (s.changeAt == 0 || s.calls >= s.changeAt) {
		return nil, s.err
	}
	requests := []VersionRequest{{Model: p.Model, RouteProvider: s.provider, ConnectionRef: p.ConnectionRef, Backend: p.Backend}}
	if s.mixed {
		requests = append(requests, VersionRequest{Model: p.Model, RouteProvider: "zhipuai", Backend: p.Backend})
	}
	if s.reverse && len(requests) == 2 {
		requests[0], requests[1] = requests[1], requests[0]
	}
	return requests, nil
}
func (s *lrPolicySource) Newest(context.Context, Profile) (EligibleCandidate, error) {
	panic("LunaRoute must not discover a following candidate")
}
func (s *lrPolicySource) ValidateStart(context.Context, Profile) (EligibleCandidate, error) {
	panic("LunaRoute must not probe a following candidate")
}

type lrPolicyStore struct {
	ProfileStore
	row    Profile
	writes int
}

func (s *lrPolicyStore) Create(_ context.Context, p Profile) error    { s.row = p; s.writes++; return nil }
func (s *lrPolicyStore) Get(context.Context, string) (Profile, error) { return s.row, nil }
func (s *lrPolicyStore) List(context.Context) ([]Profile, error)      { return []Profile{s.row}, nil }
func (s *lrPolicyStore) UpdateExpected(_ context.Context, prior, next Profile) (Profile, error) {
	if prior.Revision != s.row.Revision {
		return Profile{}, ErrAdvanceConflict
	}
	s.row = next
	s.writes++
	return next, nil
}

func (s *lrPolicyStore) DeleteExpected(context.Context, Profile) error { panic("unexpected delete") }

type lrPolicyReload struct{}

func (lrPolicyReload) Reload(context.Context) error { return nil }

func lrPolicyFixture(t *testing.T) (context.Context, *ProfileService, *lrPolicySource, *lrPolicyStore, Profile) {
	t.Helper()
	EnableLunaRouteBackendsForTest(t, "claude-code")
	glm := fullGLM()
	rules := NewCapabilityRules(map[string]BackendCapability{"claude-code": {
		Backend: "claude-code", EffortLevels: []Effort{EffortLow},
		ModelOverrides: map[string]ModelCapability{"glm-5.3": {EffortFloor: map[Effort]Effort{EffortHigh: EffortLow}}},
	}}).WithLunaRouteModels(map[string]LunaRouteModel{"glm-5.3": glm})
	src := &lrPolicySource{provider: "lunaroute"}
	store := &lrPolicyStore{}
	svc := NewProfileService(store, rules, lrPolicyReload{})
	svc.SetModelSelection(modelidentity.NewVersionResolver(), src)
	ctx := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	p := Profile{Alias: "policy", Kind: ProfileKindAgent, Backend: "claude-code", ConnectionRef: "not-a-provider-name", Model: "glm-5.3", Effort: EffortHigh}
	return ctx, svc, src, store, p
}

func TestLunaRouteEffectiveAxesNeverFloor(t *testing.T) {
	ctx, svc, _, store, p := lrPolicyFixture(t)
	saved, eff, err := svc.Create(ctx, p)
	require.NoError(t, err)
	assert.Equal(t, EffortHigh, saved.Effort)
	assert.Equal(t, EffortHigh, eff.Effort)
	assert.Equal(t, EffortHigh, store.row.Effort)
	assert.Equal(t, 1, store.writes)
	glm := fullGLM()
	glm.Efforts = nil
	svc.rules = svc.rules.WithLunaRouteModels(map[string]LunaRouteModel{p.Model: glm})
	_, _, err = svc.Create(ctx, p)
	require.ErrorIs(t, err, ErrLunaRouteAxisUnsupported)
	assert.Equal(t, 1, store.writes)
}

func TestLunaRouteEffectiveAxesUseLeasedRoute(t *testing.T) {
	ctx, svc, src, store, p := lrPolicyFixture(t)
	src.provider = "zhipuai"
	src.switchAt = 3 // selection and preliminary axes read the old connection
	saved, effective, err := svc.Create(ctx, p)
	require.NoError(t, err)
	assert.Equal(t, EffortHigh, saved.Effort)
	assert.Equal(t, EffortHigh, effective.Effort, "the leased LunaRoute route must not report an earlier native floor")
	assert.Equal(t, EffortHigh, store.row.Effort)
}

func TestLunaRouteFollowingRefusedBeforeCatalog(t *testing.T) {
	for _, model := range []string{"", "glm-5.3"} {
		t.Run(model, func(t *testing.T) {
			ctx, svc, _, store, p := lrPolicyFixture(t)
			p.Model = model
			p.SelectionMode = SelectionFollowing
			p.FollowTarget = &FollowTarget{Provider: "zhipuai", Tier: "glm"}
			_, _, err := svc.Create(ctx, p)
			require.ErrorIs(t, err, ErrLunaRoutePinnedOnly)
			assert.Zero(t, store.writes)
		})
	}
}

func TestLunaRouteUnchangedEditRechecksPolicy(t *testing.T) {
	for _, changed := range []string{"table", "connection"} {
		t.Run(changed, func(t *testing.T) {
			ctx, svc, src, store, p := lrPolicyFixture(t)
			// Stored legacy row, previously accepted with the same axes.
			store.row = p
			if changed == "table" {
				glm := fullGLM()
				glm.Backends = map[string]SupportFact{"claude-code": {Support: SupportUnsupported, Evidence: "ev#changed"}}
				svc.rules = svc.rules.WithLunaRouteModels(map[string]LunaRouteModel{p.Model: glm})
			} else {
				// A previously native route now resolves to LunaRoute, without a profile edit.
				src.provider = "zhipuai"
				p.Effort = EffortMax
				store.row = p
				_, _, err := svc.Update(ctx, p)
				require.NoError(t, err)
				store.writes = 0
				src.provider = "lunaroute"
			}
			_, _, err := svc.Update(ctx, p)
			expected := ErrLunaRouteIncompatible
			if changed == "connection" {
				expected = ErrLunaRouteAxisUnsupported
			}
			require.ErrorIs(t, err, expected)
			assert.Positive(t, src.calls)
			assert.Zero(t, store.writes)
		})
	}
}
func TestLunaRouteOtherProfileEntryPoints(t *testing.T) {
	ctx, svc, _, store, p := lrPolicyFixture(t)
	glm := fullGLM()
	glm.Tasks = map[ProfileKind]SupportFact{ProfileKindAgent: {Support: SupportUnsupported, Evidence: "ev#no-agent"}}
	svc.rules = svc.rules.WithLunaRouteModels(map[string]LunaRouteModel{p.Model: glm})
	store.row = p
	_, err := svc.prepareSeed(ctx, p)
	require.ErrorIs(t, err, ErrLunaRouteIncompatible)
	err = svc.revalidatePrepared(ctx, PreparedSelection{Profile: p})
	require.ErrorIs(t, err, ErrLunaRouteIncompatible)
	refusals, unchecked, err := svc.PinPreflight(ctx)
	require.NoError(t, err)
	require.Len(t, refusals, 1)
	assert.Empty(t, unchecked)
	require.ErrorIs(t, refusals[0].Err, ErrLunaRouteIncompatible)
	p.SelectionMode = SelectionFollowing
	p.FollowTarget = &FollowTarget{Provider: "zhipuai", Tier: "glm"}
	err = svc.ValidateAdvance(ctx, AdvanceRequest{Expected: p, Model: p.Model})
	require.ErrorIs(t, err, ErrLunaRoutePinnedOnly)
	assert.Zero(t, store.writes)
}

func TestLunaRouteMixedRoutesAndSourceErrors(t *testing.T) {
	for _, mode := range []string{"mixed", "mixed reversed", "source", "lease"} {
		t.Run(mode, func(t *testing.T) {
			ctx, svc, src, store, p := lrPolicyFixture(t)
			expected := ErrInvalidSelection
			if mode == "mixed" || mode == "mixed reversed" {
				src.mixed = true
				src.reverse = mode == "mixed reversed"
			} else {
				expected = errors.New("metadata unavailable")
				src.err = expected
				if mode == "lease" {
					src.changeAt = 3
				}
			}
			_, _, err := svc.Create(ctx, p)
			require.ErrorIs(t, err, expected)
			assert.Zero(t, store.writes)
		})
	}
}
