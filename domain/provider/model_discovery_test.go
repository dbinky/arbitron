package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type discStore struct {
	provider.ConnectionStore
	conns         map[string]provider.Connection
	key           string
	reveals, gets int
}

func (s *discStore) Get(_ context.Context, name string) (provider.Connection, error) {
	s.gets++
	c, ok := s.conns[name]
	if !ok {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return c, nil
}

func (s *discStore) Reveal(_ context.Context, name string) (string, error) {
	s.reveals++
	if _, ok := s.conns[name]; !ok {
		return "", provider.ErrConnectionNotFound
	}
	return s.key, nil
}

func checkDiscBinding(b provider.ResolvedBinding) error {
	bases := map[string]string{"lunaroute": "https://gw.lunaroute.com", "openrouter": "https://openrouter.ai/api/v1"}
	names := map[string]string{"lunaroute": "lr", "openrouter": "or"}
	if bases[b.Provider] == "" || b.BaseURL != bases[b.Provider] || b.ConnectionRef != names[b.Provider] || b.Model != "" || b.RevealKey == nil || b.Extras["fixture"] != names[b.Provider] {
		return errors.New("discovery collaborator received the wrong connection binding")
	}
	return nil
}

// The probe validates routing and actually uses the guarded credential.
type discProbe struct {
	got    provider.ResolvedBinding
	key    string
	result provider.ProbeResult
	err    error
	calls  int
}

func (p *discProbe) Probe(ctx context.Context, b provider.ResolvedBinding) (provider.ProbeResult, error) {
	p.calls++
	p.got = b
	if err := checkDiscBinding(b); err != nil {
		return provider.ProbeResult{}, err
	}
	key, err := b.RevealKey(ctx)
	if err != nil {
		return provider.ProbeResult{}, err
	}
	if key != "lr_live_disc" {
		return provider.ProbeResult{}, errors.New("wrong selected fixture credential")
	}
	p.key = key
	return p.result, p.err
}

type discSelector struct {
	probe          *discProbe
	none, nilProbe bool
	err            error
	calls          int
}

func (s *discSelector) ProbeFor(b provider.ResolvedBinding) (provider.ConnectionProbe, error) {
	s.calls++
	if err := checkDiscBinding(b); err != nil {
		return nil, err
	}
	if s.none {
		return nil, provider.ErrNoProbeForProvider
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.nilProbe {
		return nil, nil
	}
	return s.probe, nil
}

func admin() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
}

func discCaps() provider.CapabilityRules {
	return provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{
		"glm-5.3": {
			ID:              "glm-5.3",
			Tasks:           map[provider.ProfileKind]provider.SupportFact{provider.ProfileKindChat: {Support: provider.SupportSupported, Evidence: "ev#chat"}},
			Backends:        map[string]provider.SupportFact{"codex": {Support: provider.SupportUnsupported, Evidence: "ev#codex"}},
			ContextTokens:   provider.LimitFact{Tokens: 202752, Evidence: "ev#ctx"},
			Efforts:         map[string]provider.EffortFact{"chat": {Known: true, Levels: []provider.Effort{provider.EffortLow}, Evidence: "ev#eff"}, "opencode": {Known: true, Evidence: "ev#oc"}},
			Reasoning:       provider.SupportFact{Support: provider.SupportUnsupported, Evidence: "ev#reasoning"},
			RequestSettings: map[string]provider.SupportFact{"temperature": {Support: provider.SupportUnsupported, Evidence: "ev#temperature"}, "output_schema": {Support: provider.SupportSupported, Evidence: "ev#schema"}},
		},
		"kimi-k3": {ID: "kimi-k3", ContextTokens: provider.LimitFact{Tokens: 1, Evidence: "e"}},
	})
}

func lrStore() *discStore {
	return &discStore{key: "lr_live_disc", conns: map[string]provider.Connection{
		"lr": {Name: "lr", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com", Extras: map[string]string{"fixture": "lr"}},
		"or": {Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Extras: map[string]string{"fixture": "or"}},
	}}
}

func TestDiscover_LunaRouteView(t *testing.T) {
	store := lrStore()
	probe := &discProbe{result: provider.ProbeResult{Reachable: true, Models: []string{"muse-spark-open", "glm-5.3", "deepseek-4.1-flash-1017", "glm-5.3", ""}}}
	view, err := provider.NewModelDiscoveryService(store, &discSelector{probe: probe}, discCaps()).Discover(admin(), "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr", view.Connection)
	assert.Equal(t, "lunaroute", view.Provider)
	assert.True(t, view.ListingSupported)
	assert.Equal(t, provider.DiscoveryNote, view.Note)
	// One load-bearing phrase per concatenated segment (G11).
	for _, guidance := range []string{
		"Availability does not imply task or backend compatibility",
		`Fields marked "unknown" are not guarantees`, "known facts cite their evidence",
		"nothing is recommended or auto-selected", "must name their backend and use pinned selection",
		"keep doer and judge on different vendors", `an "unresolved" family is refused`,
	} {
		assert.Contains(t, view.Note, guidance)
	}
	ids := []string{}
	for _, m := range view.Models {
		ids = append(ids, m.ID)
	}
	require.Equal(t, []string{"deepseek-4.1-flash-1017", "glm-5.3", "muse-spark-open"}, ids, "sorted, deduplicated; table-only id excluded")
	glm := view.Models[1]
	assert.Equal(t, "zhipuai", glm.Family)
	assert.Equal(t, provider.FactView{Support: "supported", Evidence: "ev#chat"}, glm.Tasks["chat"])
	assert.Equal(t, provider.FactView{Support: "unknown"}, glm.Tasks["agent"])
	assert.Len(t, glm.Tasks, 2)
	assert.Equal(t, provider.FactView{Support: "unsupported", Evidence: "ev#codex"}, glm.Backends["codex"])
	assert.Equal(t, provider.FactView{Support: "unknown"}, glm.Backends["pi"])
	assert.Len(t, glm.Backends, 4)
	assert.Equal(t, provider.LimitView{Known: true, Tokens: 202752, Evidence: "ev#ctx"}, glm.ContextTokens)
	assert.Equal(t, provider.LimitView{}, glm.OutputTokens)
	assert.Equal(t, provider.EffortView{Known: true, Levels: []string{"low"}, Evidence: "ev#eff"}, glm.Efforts["chat"])
	assert.Equal(t, provider.EffortView{Known: true, Levels: []string{}, Evidence: "ev#oc"}, glm.Efforts["opencode"])
	assert.Equal(t, provider.EffortView{Levels: []string{}}, glm.Efforts["claude-code"])
	assert.Len(t, glm.Efforts, 5)
	assert.Equal(t, provider.FactView{Support: "unsupported", Evidence: "ev#reasoning"}, glm.Reasoning, "reasoning is independent of known efforts")
	assert.Equal(t, provider.FactView{Support: "unsupported", Evidence: "ev#temperature"}, glm.RequestSettings["temperature"])
	assert.Equal(t, provider.FactView{Support: "supported", Evidence: "ev#schema"}, glm.RequestSettings["output_schema"])
	assert.Len(t, glm.RequestSettings, 2)
	assert.Equal(t, "deepseek", view.Models[0].Family)
	assertUnknownDiscoveryFacts(t, view.Models[0])
	assert.Equal(t, "unresolved", view.Models[2].Family)
	assertUnknownDiscoveryFacts(t, view.Models[2])
	assert.Equal(t, "lr_live_disc", probe.key)
	assert.Equal(t, 1, store.reveals)
	assert.Equal(t, 1, probe.calls)

	raw, err := json.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "lr_live_disc")
	assert.Contains(t, string(raw), `"listing_supported":true`)
	assert.Contains(t, string(raw), `"context_tokens":{"known":true,"tokens":202752,"evidence":"ev#ctx"}`)
	assert.Contains(t, string(raw), `"reasoning":{"support":"unsupported","evidence":"ev#reasoning"}`)
	assert.Contains(t, string(raw), `"request_settings":{"output_schema":{"support":"supported","evidence":"ev#schema"},"temperature":{"support":"unsupported","evidence":"ev#temperature"}}`)
	assert.Contains(t, string(raw), `"reasoning":{"support":"unknown"}`)
	assert.Contains(t, string(raw), `"request_settings":{"output_schema":{"support":"unknown"},"temperature":{"support":"unknown"}}`)
}

func assertUnknownDiscoveryFacts(t *testing.T, m provider.DiscoveredModel) {
	t.Helper()
	unknown := provider.FactView{Support: "unknown"}
	assert.Equal(t, map[string]provider.FactView{"chat": unknown, "agent": unknown}, m.Tasks)
	assert.Equal(t, map[string]provider.FactView{"claude-code": unknown, "opencode": unknown, "codex": unknown, "pi": unknown}, m.Backends)
	assert.Equal(t, provider.LimitView{}, m.ContextTokens)
	assert.Equal(t, provider.LimitView{}, m.OutputTokens)
	assert.Equal(t, unknown, m.Reasoning)
	assert.Equal(t, map[string]provider.FactView{"temperature": unknown, "output_schema": unknown}, m.RequestSettings)
	assert.Len(t, m.Efforts, 5)
	for _, axis := range []string{"chat", "claude-code", "opencode", "codex", "pi"} {
		assert.Equal(t, provider.EffortView{Levels: []string{}}, m.Efforts[axis])
	}
}

func TestDiscover_GuardAppliesToDiscoveryBinding(t *testing.T) {
	store := lrStore()
	store.key = "sk-or-v1-foreign"
	probe := &discProbe{result: provider.ProbeResult{Reachable: true}}
	view, err := provider.NewModelDiscoveryService(store, &discSelector{probe: probe}, discCaps()).Discover(admin(), "lr")
	require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
	assert.Empty(t, probe.key)
	assert.Equal(t, provider.DiscoveryView{}, view)
	assert.NotContains(t, err.Error(), store.key)
}

func TestDiscover_NonLunaRouteProviderIsAllUnknown(t *testing.T) {
	probe := &discProbe{result: provider.ProbeResult{Reachable: true, Models: []string{"glm-5.3", "anthropic/claude-sonnet-4.5"}}}
	view, err := provider.NewModelDiscoveryService(lrStore(), &discSelector{probe: probe}, discCaps()).Discover(admin(), "or")
	require.NoError(t, err)
	require.Len(t, view.Models, 2)
	assert.Equal(t, "anthropic", view.Models[0].Family)
	assert.Equal(t, "zhipuai", view.Models[1].Family)
	for _, m := range view.Models {
		assertUnknownDiscoveryFacts(t, m)
	}
}

func TestDiscover_ListingUnsupported(t *testing.T) {
	for _, noProbe := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "no probe"}[noProbe], func(t *testing.T) {
			store := lrStore()
			probe := &discProbe{result: provider.ProbeResult{Reachable: true, ModelsUnsupported: true, Models: []string{"ignored"}}}
			view, err := provider.NewModelDiscoveryService(store, &discSelector{probe: probe, none: noProbe}, discCaps()).Discover(admin(), "or")
			require.NoError(t, err)
			assert.False(t, view.ListingSupported)
			assert.NotNil(t, view.Models)
			assert.Empty(t, view.Models)
			assert.Equal(t, "or", view.Connection)
			assert.Equal(t, provider.DiscoveryNote, view.Note)
			if noProbe {
				assert.Zero(t, store.reveals)
				assert.Zero(t, probe.calls)
			}
		})
	}
}

func TestDiscover_EmptyListing(t *testing.T) {
	probe := &discProbe{result: provider.ProbeResult{Reachable: true}}
	view, err := provider.NewModelDiscoveryService(lrStore(), &discSelector{probe: probe}, discCaps()).Discover(admin(), "lr")
	require.NoError(t, err)
	assert.True(t, view.ListingSupported)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"models":[]`)
}

func TestDiscover_ProbeFailurePropagates(t *testing.T) {
	failure := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Status: 401})
	probe := &discProbe{err: failure}
	view, err := provider.NewModelDiscoveryService(lrStore(), &discSelector{probe: probe}, discCaps()).Discover(admin(), "lr")
	require.ErrorIs(t, err, provider.ErrLunaRouteCredentialsRejected)
	assert.Same(t, failure, err)
	assert.Equal(t, provider.DiscoveryView{}, view)
}

func TestDiscover_AuthzAndNotFound(t *testing.T) {
	store := lrStore()
	sel := &discSelector{probe: &discProbe{}}
	svc := provider.NewModelDiscoveryService(store, sel, discCaps())
	_, err := svc.Discover(identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleUser}), "lr")
	require.ErrorIs(t, err, provider.ErrForbidden)
	_, err = svc.Discover(context.Background(), "lr")
	require.ErrorIs(t, err, provider.ErrUnauthenticated)
	assert.Zero(t, store.gets)
	assert.Zero(t, store.reveals)
	assert.Zero(t, sel.calls)
	_, err = svc.Discover(admin(), "ghost")
	require.ErrorIs(t, err, provider.ErrConnectionNotFound)
	assert.Zero(t, sel.calls)
	_, err = provider.NewModelDiscoveryService(nil, nil, nil).Discover(context.Background(), "lr")
	require.ErrorIs(t, err, provider.ErrUnauthenticated, "authorization precedes wiring checks")
}

func TestDiscover_NilPortsAreWiringFaults(t *testing.T) {
	for name, svc := range map[string]*provider.ModelDiscoveryService{
		"store":  provider.NewModelDiscoveryService(nil, &discSelector{}, discCaps()),
		"probes": provider.NewModelDiscoveryService(lrStore(), nil, discCaps()),
		"caps":   provider.NewModelDiscoveryService(lrStore(), &discSelector{}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			view, err := svc.Discover(admin(), "lr")
			require.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
			assert.Equal(t, provider.DiscoveryView{}, view)
		})
	}
}

func TestDiscover_UntrustworthyProbeResults(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		probe := &discProbe{result: provider.ProbeResult{ModelsUnsupported: unsupported, Models: []string{"glm-5.3"}}}
		view, err := provider.NewModelDiscoveryService(lrStore(), &discSelector{probe: probe}, discCaps()).Discover(admin(), "lr")
		require.ErrorIs(t, err, provider.ErrLunaRouteServiceFailure)
		assert.Equal(t, provider.DiscoveryView{}, view)
	}
	view, err := provider.NewModelDiscoveryService(lrStore(), &discSelector{nilProbe: true}, discCaps()).Discover(admin(), "lr")
	require.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
	assert.Equal(t, provider.DiscoveryView{}, view)
}

func TestDiscover_SelectorFailurePropagates(t *testing.T) {
	failure := errors.New("selector failed")
	store := lrStore()
	view, err := provider.NewModelDiscoveryService(store, &discSelector{err: failure}, discCaps()).Discover(admin(), "lr")
	assert.Same(t, failure, err)
	assert.Equal(t, provider.DiscoveryView{}, view)
	assert.Zero(t, store.reveals)
}

func TestDiscover_RevokedAliasFamily(t *testing.T) {
	probe := &discProbe{result: provider.ProbeResult{Reachable: true, Models: []string{"claude-sonnet-5-lr1", "opaque-lr999"}}}
	view, err := provider.NewModelDiscoveryService(lrStore(), &discSelector{probe: probe}, discCaps()).Discover(admin(), "lr")
	require.NoError(t, err)
	require.Len(t, view.Models, 2)
	assert.Equal(t, "unresolved", view.Models[0].Family, "R3: listing cannot turn an open-weight alias into Anthropic")
	assert.Equal(t, "unresolved", view.Models[1].Family)
}
