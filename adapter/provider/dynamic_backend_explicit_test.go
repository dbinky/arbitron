package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lrTable map[string]provider.LunaRouteModel

func (t lrTable) LunaRouteModel(id string) (provider.LunaRouteModel, bool) {
	m, ok := t[id]
	return m, ok
}

func lrAgentTable() lrTable {
	return lrTable{"glm-5.3": {
		ID:            "glm-5.3",
		ContextTokens: provider.LimitFact{Tokens: 200000, Evidence: "e#context"},
		OutputTokens:  provider.LimitFact{Tokens: 32768, Evidence: "e#output"},
		Reasoning:     provider.SupportFact{Support: provider.SupportSupported, Evidence: "e#reasoning"},
	}}
}

// BackendExplicit is author intent: true only when the PROFILE names a backend
// (whitespace does not count), on both resolver arms. The deployment default is
// applied later and must never be able to make a binding explicit.
func TestDynamicProfileResolver_BackendExplicitProvenance(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: provider.LunaRouteHostedRoot}, "lr_k")
	for _, tc := range []struct {
		name    string
		backend string
		want    bool
	}{
		{"named", "pi", true},
		{"empty", "", false},
		{"whitespace only", "  \t", false},
	} {
		for _, arm := range []struct{ name, ref string }{{"subscription arm", ""}, {"connection arm", "lr"}} {
			t.Run(tc.name+"/"+arm.name, func(t *testing.T) {
				r := newDynamicResolver(t, []provider.Profile{
					{Alias: "agent", Kind: provider.ProfileKindAgent, ConnectionRef: arm.ref, Model: "glm-5.3", Backend: tc.backend},
				}, store)
				b, err := r.Resolve(context.Background(), "agent")
				require.NoError(t, err)
				assert.Equal(t, tc.want, b.BackendExplicit)
			})
		}
	}
}

// A binding produced by the real resolver reaches the launch gate with its
// provenance intact: an explicit pi profile passes it; a backend-less LunaRoute
// profile whose effective backend came from a deployment default is refused.
func TestDynamicProfileResolver_BackendProvenanceReachesLaunchGate(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: provider.LunaRouteHostedRoot}, "lr_k")
	r := newDynamicResolver(t, []provider.Profile{
		{Alias: "explicit", Kind: provider.ProfileKindAgent, ConnectionRef: "lr", Model: "glm-5.3", Backend: "pi"},
		{Alias: "legacy", Kind: provider.ProfileKindAgent, ConnectionRef: "lr", Model: "glm-5.3"},
	}, store)
	caps := lrAgentTable()

	explicit, err := r.Resolve(context.Background(), "explicit")
	require.NoError(t, err)
	require.NoError(t, provider.CheckLunaRouteLaunch(caps, explicit, 0))

	legacy, err := r.Resolve(context.Background(), "legacy")
	require.NoError(t, err)
	// The executor applies the deployment default AFTER resolution; it must not
	// turn the binding explicit.
	legacy.Backend = "pi"
	err = provider.CheckLunaRouteLaunch(caps, legacy, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, provider.ErrLunaRouteBackendRequired), "got %v", err)
}
