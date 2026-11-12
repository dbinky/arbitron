package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/opencode"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingSelector struct{ calls int }

func (s *countingSelector) ProbeFor(provider.ResolvedBinding) (provider.ConnectionProbe, error) {
	s.calls++
	return nil, provider.ErrNoProbeForProvider
}
func TestFactory_AgentProviderFor_WrapsLunaRouteOnly(t *testing.T) {
	probes := &countingSelector{}
	f := adapterprovider.NewFactory(adapterprovider.Deps{Probes: probes})
	assert.Same(t, probes, f.ProbeSelector())
	ap, err := f.AgentProviderFor(provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "openrouter", Backend: "opencode", Model: "m"})
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
	reveals := 0
	b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com", Model: "glm-5.3", Backend: "opencode", BackendExplicit: true, ContextWindow: provider.Context1M, ConnectionRef: "lr", RevealKey: func(context.Context) (string, error) { reveals++; return "lr_k", nil }}
	ap, err = f.AgentProviderFor(b)
	require.NoError(t, err, "construction is lazy")
	_, unwrapped := ap.(*opencode.Provider)
	assert.False(t, unwrapped)
	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "nonce", WorkDir: t.TempDir()})
	require.ErrorIs(t, err, provider.ErrLunaRouteAxisUnsupported)
	assert.Zero(t, reveals)
	assert.Zero(t, probes.calls)
	b.Backend = ""
	_, err = f.AgentProviderFor(b)
	require.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
}
