package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/azurefoundry"
	"github.com/arbitron/arbitron/adapter/bedrock"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinningD8Selectors(t *testing.T) {
	factory := adapterprovider.NewFactory(adapterprovider.Deps{})
	for _, route := range []string{"azure-openai", "azure-foundry", "azure", "bedrock", "amazon-bedrock"} {
		t.Run(route, func(t *testing.T) {
			builds := 0
			build := func(b provider.ResolvedBinding) engine.ChatProvider {
				builds++
				assert.Equal(t, route, b.Provider)
				assert.Equal(t, provider.Canonicalize(route), b.OpaquePinRoute)
				assert.True(t, b.Pinned)
				assert.NotEmpty(t, b.Model)
				return &sentinelChatProvider{name: b.Backend}
			}
			selector := adapterprovider.NewInteractiveStreamSelector(factory, map[string]adapterprovider.AgentStreamFactory{
				"codex": build, "claude-code": build, "pi": build,
			}, "codex")
			binding := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: route, Model: "configured-ID", Pinned: true, OpaquePinRoute: provider.Canonicalize(route)}
			for _, selectChat := range []func(provider.ResolvedBinding) (engine.ChatProvider, error){factory.ChatProviderFor, selector.StreamProviderFor} {
				p, err := selectChat(binding)
				require.NoError(t, err)
				require.NotNil(t, p)
				// Explicit proof remains stricter than the native execution exemption,
				// for synchronous and streaming calls, before even credential access.
				req := engine.ChatRequest{Model: binding.Model, Prompt: "probe", RequireExecutedModel: true}
				expected := azurefoundry.ErrModelExecutionEvidenceUnavailable
				if provider.Canonicalize(route) == provider.CanonicalBedrock {
					expected = bedrock.ErrModelExecutionEvidenceUnavailable
				}
				response, err := p.Chat(context.Background(), req)
				require.ErrorIs(t, err, expected)
				assert.Nil(t, response)
				chunks, err := p.StreamChat(context.Background(), req)
				require.ErrorIs(t, err, expected)
				assert.Nil(t, chunks)
				otherFamily := provider.CanonicalAzure
				if binding.OpaquePinRoute == provider.CanonicalAzure {
					otherFamily = provider.CanonicalBedrock
				}
				for _, bad := range []string{"", "openai", "azure-openai", otherFamily} {
					invalid := binding
					invalid.OpaquePinRoute = bad
					p, err = selectChat(invalid)
					require.Error(t, err)
					assert.Nil(t, p)
				}
			}
			// An exception belongs to native chat/stream adapters, never a harness,
			// even if the opaque string happens to resemble its fixed-model spelling.
			binding.Kind = provider.ProfileKindAgent
			for _, backend := range []string{"claude-code", "codex", "pi", "opencode"} {
				binding.Backend = backend
				binding.Model = "claude-sonnet-4-6"
				if backend == "codex" {
					binding.Model = "gpt-6-sol"
				}
				p, err := factory.AgentProviderFor(binding)
				require.Error(t, err)
				assert.Nil(t, p)
				stream, err := selector.StreamProviderFor(binding)
				require.Error(t, err)
				assert.Nil(t, stream)
			}
			assert.Zero(t, builds, "opaque exceptions must fail before constructing a harness session")
		})
	}
}
