package provider_test

import (
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinningSelectorsRejectIncompatibleLiveRoute(t *testing.T) {
	factory := adapterprovider.NewFactory(adapterprovider.Deps{})
	for _, route := range []string{"azure-openai", "azure-foundry", "openai"} {
		t.Run(route, func(t *testing.T) {
			b := domprovider.ResolvedBinding{Kind: domprovider.ProfileKindChat, Provider: route, Model: "deepseek/deepseek-v4-flash", Pinned: true}
			p, err := factory.ChatProviderFor(b)
			assert.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
			assert.Nil(t, p)
			// Historical runs intentionally retain live routing semantics.
			b.Pinned = false
			p, err = factory.ChatProviderFor(b)
			require.NoError(t, err)
			assert.NotNil(t, p)
		})
	}

	// Chat dispatch ignores the backend axis; it must never gain the Codex
	// subscription exception merely because a backend field is present.
	chat, err := factory.ChatProviderFor(domprovider.ResolvedBinding{Kind: domprovider.ProfileKindChat, Provider: "azure-openai", Backend: "codex", Model: "gpt-6-sol", Pinned: true})
	assert.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
	assert.Nil(t, chat)

	b := domprovider.ResolvedBinding{Kind: domprovider.ProfileKindAgent, Provider: "claude-cli", Backend: "codex", Model: "claude-sonnet-4-6", Pinned: true}
	p, err := factory.AgentProviderFor(b)
	assert.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
	assert.Nil(t, p)

	builds := 0
	selected := &sentinelChatProvider{name: "codex"}
	selector := adapterprovider.NewInteractiveStreamSelector(factory, map[string]adapterprovider.AgentStreamFactory{
		"codex": func(binding domprovider.ResolvedBinding) engine.ChatProvider {
			builds++
			assert.Equal(t, "codex", binding.Backend)
			assert.Equal(t, "gpt-6-sol", binding.Model)
			return selected
		},
	}, "codex")
	b.Backend = "" // validate AFTER applying the deployment default
	stream, err := selector.StreamProviderFor(b)
	assert.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
	assert.Nil(t, stream)
	assert.Zero(t, builds, "incompatible model must fail before constructing a session")

	// Codex uses its own account, not the otherwise live connection provider.
	b.Provider = "azure-openai"
	b.Model = "gpt-6-sol"
	b.Backend = "codex"
	p, err = factory.AgentProviderFor(b)
	require.NoError(t, err)
	assert.NotNil(t, p)
	b.Backend = ""
	stream, err = selector.StreamProviderFor(b)
	require.NoError(t, err)
	assert.Same(t, selected, stream)
	assert.Equal(t, 1, builds)
}

// A no-connection pi profile arrives with the subscription stamp "claude-cli";
// the live-route preflight must judge its model by pi's vendor prefix, not by
// the Claude subscription grammar.
func TestPinningSelectorsAcceptPiSubscriptionRoute(t *testing.T) {
	factory := adapterprovider.NewFactory(adapterprovider.Deps{})
	b := domprovider.ResolvedBinding{Kind: domprovider.ProfileKindAgent, Provider: "claude-cli", Backend: "pi", Model: "openai-codex/gpt-6.1-sol", Pinned: true}
	_, err := factory.AgentProviderFor(b)
	assert.NotErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)

	b.Model = "openai-codex/gpt-6-latest"
	p, err := factory.AgentProviderFor(b)
	assert.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
	assert.Nil(t, p)
}
