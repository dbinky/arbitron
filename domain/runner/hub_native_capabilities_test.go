package runner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeAgentCap(backend string, fams []modelidentity.Family, model string) runner.ExecutionCapability {
	return runner.ExecutionCapability{Op: runner.OpAgent, Backend: backend, Provider: "claude-cli", EndpointFingerprint: strings.Repeat("b", 64), Families: fams, Models: []string{model}, ContextWindows: []provider.ContextWindow{provider.ContextDefault, provider.Context200K}, Efforts: []provider.Effort{provider.EffortDefault, provider.EffortHigh}, Tools: []string{"Read", "Grep"}, ExecutedModel: true}
}

func TestHubAdmit_AcceptsNativeAgentCapabilityOnSameSource(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	claude := hubtest.AnthropicSource()
	claude.Backends = []string{"", "claude-code"}
	claude.Capabilities = []runner.ExecutionCapability{
		{Op: runner.OpChat, Provider: "claude-cli", EndpointFingerprint: strings.Repeat("a", 64), Families: claude.Families, Models: []string{"claude-opus-5-5"}, ExecutedModel: true},
		nativeAgentCap("claude-code", claude.Families, "claude-opus-5-5"),
	}
	codex := runner.SourceReport{Kind: runner.CredOpenAISubscription, Families: []modelidentity.Family{"openai"}, Backends: []string{"codex"}, Models: []string{"gpt-6.1-sol"}, Capabilities: []runner.ExecutionCapability{nativeAgentCap("codex", []modelidentity.Family{"openai"}, "gpt-6.1-sol")}}
	admit(t, p, h, "mac-a", hubtest.DarwinHello(claude, codex))
	_, sources, err := p.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	require.Len(t, sources, 2)
	byKind := map[runner.CredentialKind]runner.Source{}
	for _, s := range sources {
		byKind[s.Kind] = s
	}
	assert.Equal(t, claude.Capabilities, byKind[runner.CredAnthropicSubscription].Capabilities)
	assert.Equal(t, codex.Capabilities, byKind[runner.CredOpenAISubscription].Capabilities)
}

func TestHubAdmit_RejectsAgentCapabilityWithoutNativeBackend(t *testing.T) {
	for _, backend := range []string{"", "opencode", "pi", "Claude-Code"} {
		t.Run("backend="+backend, func(t *testing.T) {
			p := hubtest.NewPool(t)
			h := p.NewHub(hubtest.HubConfig())
			key := p.SeedRunner(t, "mac-a", 2)
			src := hubtest.AnthropicSource()
			src.Capabilities = []runner.ExecutionCapability{nativeAgentCap(backend, src.Families, "claude-opus-5-5")}
			_, err := h.Admit(context.Background(), key, hubtest.NewFakeConn(), hubtest.DarwinHello(src))
			require.ErrorIs(t, err, runner.ErrMalformedMessage)
			_, sources, err := p.Registry.GetRunner(context.Background(), "mac-a")
			require.NoError(t, err)
			assert.Empty(t, sources)
		})
	}
}
