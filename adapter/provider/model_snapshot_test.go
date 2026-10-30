package provider_test

import (
	"context"
	"errors"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSnapshotter(t *testing.T, store *fakeConnectionStore, profiles ...provider.Profile) (*adapterprovider.ProfileCache, *adapterprovider.ModelSnapshotter) {
	t.Helper()
	cache := adapterprovider.NewProfileCacheFromProfiles(profiles...)
	f := newInvFixture()
	var connections provider.ConnectionStore
	if store != nil {
		f.conns.fakeConnectionStore = *store
		connections = store
	}
	return cache, adapterprovider.NewModelSnapshotter(cache, connections, modelidentity.NewVersionResolver(), f.build(t))
}

func TestSnapshotD8RouteValidation(t *testing.T) {
	for _, route := range []string{"azure-openai", "azure-foundry", "azure", "bedrock", "amazon-bedrock"} {
		for _, mode := range []provider.SelectionMode{provider.SelectionPinned, provider.SelectionFollowing} {
			t.Run(route+"/"+string(mode), func(t *testing.T) {
				f := newInvFixture()
				f.addConn(provider.Connection{Name: "native", Provider: route, BaseURL: "https://native.example", Extras: map[string]string{"api-version": "2024-10-21", "region": "us-east-1"}})
				p := provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, ConnectionRef: "native", Model: "Configured/Deployment:ID", SelectionMode: mode}
				if mode == provider.SelectionFollowing {
					p.FollowTarget = &provider.FollowTarget{Provider: "openai", Tier: "sol"}
				}
				cache := adapterprovider.NewProfileCacheFromProfiles(p)
				snap := adapterprovider.NewModelSnapshotter(cache, f.conns, modelidentity.NewVersionResolver(), f.build(t))
				wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "call", Type: workflow.NodeTypeLLMCall, Config: workflow.NodeConfig{ModelProfile: "a"}}}}
				got, err := snap.SnapshotModels(context.Background(), []string{"a"}, wf)
				if mode == provider.SelectionFollowing {
					require.Error(t, err)
					assert.Empty(t, got.Models)
					assert.Empty(t, got.OpaqueRoutes)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"a": p.Model}, got.Models)
				assert.Equal(t, map[string]string{"a": provider.Canonicalize(route)}, got.OpaqueRoutes)
				got.Models["a"], got.OpaqueRoutes["a"] = "mutated", "openai"
				again, err := snap.SnapshotModels(context.Background(), []string{"a"}, wf)
				require.NoError(t, err)
				assert.Equal(t, p.Model, again.Models["a"])
				assert.Equal(t, provider.Canonicalize(route), again.OpaqueRoutes["a"])
			})
		}
	}
}

func TestSnapshotMissingVersionsFails(t *testing.T) {
	f := newInvFixture()
	cache := adapterprovider.NewProfileCacheFromProfiles(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	snap := adapterprovider.NewModelSnapshotter(cache, nil, nil, f.build(t))
	for _, aliases := range [][]string{nil, {"a"}} {
		got, err := snap.SnapshotModels(context.Background(), aliases, &workflow.Workflow{})
		require.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
		assert.Empty(t, got)
	}
}

func TestSnapshotFollowingCannotUseFixedBedrockSpelling(t *testing.T) {
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "native", Provider: "bedrock", Extras: map[string]string{"region": "us-east-1"}})
	p := provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, ConnectionRef: "native", Model: "anthropic.claude-sonnet-4-5-20250929-v1:0", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}}
	snap := adapterprovider.NewModelSnapshotter(adapterprovider.NewProfileCacheFromProfiles(p), f.conns, modelidentity.NewVersionResolver(), f.build(t))
	got, err := snap.SnapshotModels(context.Background(), []string{"a"}, &workflow.Workflow{})
	require.ErrorIs(t, err, provider.ErrInvalidSelection)
	assert.Empty(t, got)
}

func TestSnapshotSpecificVersionsMustAgree(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(map[bool]string{false: "consistent", true: "conflicting"}[different], func(t *testing.T) {
			f := newInvFixture()
			f.addConn(provider.Connection{Name: "route", Provider: "openrouter"})
			p := provider.Profile{Alias: "a", Kind: provider.ProfileKindAgent, ConnectionRef: "route", Backend: "pi", Model: "deepseek/deepseek-v4-flash"}
			wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "other", Type: workflow.NodeTypeAgentCall, Config: workflow.NodeConfig{ModelProfile: "a", Backend: "opencode"}}}}
			versions := &snapshotVersions{profile: p, different: different}
			snap := adapterprovider.NewModelSnapshotter(adapterprovider.NewProfileCacheFromProfiles(p), f.conns, versions, f.build(t))
			got, err := snap.SnapshotModels(context.Background(), []string{"a"}, wf)
			if different {
				require.Error(t, err)
				assert.Empty(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"a": "deepseek/deepseek-v4.1-flash"}, got.Models)
				assert.Empty(t, got.OpaqueRoutes)
			}
			assert.ElementsMatch(t, []string{"pi", "opencode"}, versions.backends)
		})
	}
}

// Check every load-bearing route argument; this unit port exercises the version
// contract while the actual inventory supplies the captured workflow routes.
type snapshotVersions struct {
	profile   provider.Profile
	different bool
	backends  []string
}

func (v *snapshotVersions) SpecificVersion(_ context.Context, request provider.VersionRequest) (string, error) {
	if request.Model != v.profile.Model || request.ConnectionRef != v.profile.ConnectionRef || request.RouteProvider != "openrouter" || request.Backend != "pi" && request.Backend != "opencode" {
		return "", errors.New("unexpected captured version request")
	}
	v.backends = append(v.backends, request.Backend)
	if v.different && request.Backend == "pi" {
		return "deepseek/deepseek-v4.2-flash", nil
	}
	return "deepseek/deepseek-v4.1-flash", nil
}

func TestSnapshotCodexCannotInheritD8ConnectionException(t *testing.T) {
	f := newInvFixture()
	f.backend = workflow.BackendCodex
	f.addConn(provider.Connection{Name: "native", Provider: "azure", BaseURL: "https://native.example"})
	for _, model := range []string{"gpt-6.1-sol", "Configured/Deployment:ID"} {
		t.Run(model, func(t *testing.T) {
			p := provider.Profile{Alias: "a", Kind: provider.ProfileKindAgent, ConnectionRef: "native", Model: model}
			snap := adapterprovider.NewModelSnapshotter(adapterprovider.NewProfileCacheFromProfiles(p), f.conns, modelidentity.NewVersionResolver(), f.build(t))
			got, err := snap.SnapshotModels(context.Background(), []string{"a"}, &workflow.Workflow{})
			if model == "gpt-6.1-sol" {
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"a": model}, got.Models)
				assert.Empty(t, got.OpaqueRoutes)
			} else {
				require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
				assert.Empty(t, got.Models)
				assert.Empty(t, got.OpaqueRoutes)
			}
		})
	}
}

func TestModelSnapshotter_PinsSubscriptionAndConnectionProfiles(t *testing.T) {
	conns := newFakeConnectionStore()
	conns.put(provider.Connection{Name: "or", Provider: "openrouter"}, "k")
	_, snap := newSnapshotter(t, conns,
		provider.Profile{Alias: "sub", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"},
		provider.Profile{Alias: "routed", Kind: provider.ProfileKindChat, ConnectionRef: "or", Model: "deepseek/deepseek-v4-flash"},
	)

	got, err := snap.SnapshotModels(context.Background(), []string{"sub", "routed"}, &workflow.Workflow{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"sub": "claude-opus-4-1", "routed": "deepseek/deepseek-v4-flash"}, got.Models)
	assert.Empty(t, got.OpaqueRoutes)
}

func TestModelSnapshotter_EmptyAliasesYieldsEmptyNonNilMap(t *testing.T) {
	_, snap := newSnapshotter(t, nil)
	got, err := snap.SnapshotModels(context.Background(), nil, &workflow.Workflow{})
	require.NoError(t, err)
	require.NotNil(t, got.Models)
	require.NotNil(t, got.OpaqueRoutes)
	assert.Empty(t, got.Models)
	assert.Empty(t, got.OpaqueRoutes)
}

func TestModelSnapshotter_RejectsNilCacheAtConstruction(t *testing.T) {
	f := newInvFixture()
	routes := f.build(t)
	assert.PanicsWithValue(t, "provider: NewModelSnapshotter requires a non-nil profile cache", func() {
		adapterprovider.NewModelSnapshotter(nil, nil, modelidentity.NewVersionResolver(), routes)
	})
}

func TestModelSnapshotter_NilWorkflowFails(t *testing.T) {
	_, snap := newSnapshotter(t, nil, provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	for _, aliases := range [][]string{nil, {"a"}} {
		got, err := snap.SnapshotModels(context.Background(), aliases, nil)
		require.ErrorContains(t, err, "workflow is required")
		assert.Empty(t, got)
	}
}

func TestModelSnapshotter_UnknownAliasFails(t *testing.T) {
	_, snap := newSnapshotter(t, nil, provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	_, err := snap.SnapshotModels(context.Background(), []string{"a", "ghost"}, &workflow.Workflow{})
	require.ErrorIs(t, err, provider.ErrUnknownProfile)
	assert.Contains(t, err.Error(), "ghost")
}

func TestModelSnapshotter_MutableAliasAsksForFixedVersion(t *testing.T) {
	for _, model := range []string{"sonnet", "", "latest", "production-v2"} {
		t.Run(model, func(t *testing.T) {
			_, snap := newSnapshotter(t, nil, provider.Profile{Alias: "m", Kind: provider.ProfileKindChat, Model: model})
			got, err := snap.SnapshotModels(context.Background(), []string{"m"}, &workflow.Workflow{})
			require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), `"m"`)
			assert.Contains(t, err.Error(), "fixed model version")
		})
	}
}

func TestModelSnapshotter_MissingConnectionFails(t *testing.T) {
	_, snap := newSnapshotter(t, newFakeConnectionStore(),
		provider.Profile{Alias: "r", Kind: provider.ProfileKindChat, ConnectionRef: "gone", Model: "gpt-4o-2024-08-06"})
	_, err := snap.SnapshotModels(context.Background(), []string{"r"}, &workflow.Workflow{})
	require.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

func TestModelSnapshotter_NilConnectionStoreForRoutedProfileFails(t *testing.T) {
	_, snap := newSnapshotter(t, nil,
		provider.Profile{Alias: "r", Kind: provider.ProfileKindChat, ConnectionRef: "or", Model: "gpt-4o-2024-08-06"})
	_, err := snap.SnapshotModels(context.Background(), []string{"r"}, &workflow.Workflow{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no connection store")
}

func TestModelSnapshotter_CancelledContextFails(t *testing.T) {
	_, snap := newSnapshotter(t, nil, provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := snap.SnapshotModels(ctx, []string{"a"}, &workflow.Workflow{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestModelSnapshotter_ActualRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, model, backend string
		wantErr              bool
	}{
		{"deployment Codex", "gpt-5-sol", workflow.BackendCodex, false},
		{"Claude model on Codex", "claude-opus-4-1", workflow.BackendCodex, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInvFixture()
			f.backend = tc.backend
			// Route-only pin validation must not read policy or rediscover models.
			f.policy.err = errors.New("policy unavailable")
			p := provider.Profile{Alias: "a", Kind: provider.ProfileKindAgent, Model: tc.model}
			cache := adapterprovider.NewProfileCacheFromProfiles(p)
			snap := adapterprovider.NewModelSnapshotter(cache, nil, modelidentity.NewVersionResolver(), f.build(t))
			got, err := snap.SnapshotModels(context.Background(), []string{"a"}, &workflow.Workflow{})
			if tc.wantErr {
				require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
				assert.Empty(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"a": tc.model}, got.Models)
			}
		})
	}
}

// The live coder-pi-sol-high shape: backend pi, no connection, the vendor named
// in pi's model prefix. Pinning must use the prefixed vendor's grammar.
func TestModelSnapshotter_PiSubscriptionProfile(t *testing.T) {
	for _, tc := range []struct {
		model   string
		wantErr bool
	}{
		{"openai-codex/gpt-6.1-sol", false},
		{"anthropic/claude-sonnet-5-5", true},
		{"openai-codex/gpt-6-latest", true},
		{"google/gemini-3-pro", true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			f := newInvFixture()
			p := provider.Profile{Alias: "coder-pi-sol-high", Kind: provider.ProfileKindAgent, Backend: workflow.BackendPi, Model: tc.model}
			cache := adapterprovider.NewProfileCacheFromProfiles(p)
			snap := adapterprovider.NewModelSnapshotter(cache, nil, modelidentity.NewVersionResolver(), f.build(t))
			wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "implement", Type: workflow.NodeTypeAgentCall, Config: workflow.NodeConfig{ModelProfile: p.Alias}}}}
			got, err := snap.SnapshotModels(context.Background(), []string{p.Alias}, wf)
			if tc.wantErr {
				require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
				assert.Contains(t, err.Error(), "backend pi")
				assert.Empty(t, got.Models)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, map[string]string{p.Alias: tc.model}, got.Models)
		})
	}
}

func TestModelSnapshotter_UnrelatedReadFaultDoesNotStopSnapshot(t *testing.T) {
	f := newInvFixture()
	boom := errors.New("workflow read fault")
	f.wfs = &invWorkflows{scopeErr: boom}
	cache := adapterprovider.NewProfileCacheFromProfiles(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	snap := adapterprovider.NewModelSnapshotter(cache, nil, modelidentity.NewVersionResolver(), f.build(t))
	got, err := snap.SnapshotModels(context.Background(), []string{"a"}, &workflow.Workflow{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "claude-opus-4-1"}, got.Models)
}

func TestModelSnapshotter_MissingRouteSourceFails(t *testing.T) {
	cache := adapterprovider.NewProfileCacheFromProfiles(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	snap := adapterprovider.NewModelSnapshotter(cache, nil, modelidentity.NewVersionResolver(), nil)
	got, err := snap.SnapshotModels(context.Background(), []string{"a"}, &workflow.Workflow{})
	require.ErrorContains(t, err, "route source")
	assert.Empty(t, got)
}

// SnapshotProfiles returns one generation as clones: mutating the result must
// not reach the live cache, and every alias comes from the same generation.
func TestProfileCache_SnapshotProfilesReturnsIndependentClones(t *testing.T) {
	cache := adapterprovider.NewProfileCacheFromProfiles(
		provider.Profile{Alias: "a", Kind: provider.ProfileKindAgent, Model: "claude-opus-4-1", AllowedTools: []string{"Read"}, FollowTarget: &provider.FollowTarget{Provider: "anthropic", Tier: "opus"}},
	)
	snap, err := cache.SnapshotProfiles([]string{"a"})
	require.NoError(t, err)
	snap["a"].AllowedTools[0] = "Bash"
	snap["a"].FollowTarget.Provider = "openai"
	delete(snap, "a")

	again, err := cache.SnapshotProfiles([]string{"a"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Read"}, again["a"].AllowedTools)
	assert.Equal(t, &provider.FollowTarget{Provider: "anthropic", Tier: "opus"}, again["a"].FollowTarget)
}

func TestProfileCache_SnapshotProfilesUnknownAliasIsAllOrNothing(t *testing.T) {
	cache := adapterprovider.NewProfileCacheFromProfiles(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "m-1"})
	got, err := cache.SnapshotProfiles([]string{"a", "ghost"})
	require.ErrorIs(t, err, provider.ErrUnknownProfile)
	assert.Nil(t, got)
}
