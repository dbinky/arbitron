package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
)

func pinnedChainBinding(transport *fakeTransport, profiles ...provider.Profile) binding {
	return binding{
		name:         "pinned-chain",
		workflowName: "mixed-chat-providers",
		profiles:     profiles,
		connections: []seedConn{
			{conn: provider.Connection{Name: "ollama-local", Provider: "ollama-cloud", BaseURL: "https://ollama.example/v1"}},
			{conn: provider.Connection{Name: "anthropic-chat", Provider: "anthropic-api", BaseURL: "https://api.anthropic.example"}, key: "sk-anthropic"},
			{conn: provider.Connection{Name: "openai-chat", Provider: "openai", BaseURL: "https://api.openai.example/v1"}, key: "sk-openai"},
		},
		runner:    newFakeRunner(),
		transport: transport,
	}
}

func pinnedChainProfiles() []provider.Profile {
	return []provider.Profile{
		{Alias: "drafter", Kind: provider.ProfileKindChat, ConnectionRef: "ollama-local", Model: "deepseek-v4-flash"},
		{Alias: "reviewer", Kind: provider.ProfileKindChat, ConnectionRef: "anthropic-chat", Model: "claude-sonnet-4-5"},
		{Alias: "summarizer", Kind: provider.ProfileKindChat, ConnectionRef: "openai-chat", Model: "gpt-6.1-sol"},
	}
}

// TestAcceptance_09_PinnedRunKeepsTriggerModels drives the real pin path end to
// end: the real snapshotter pins the models at trigger, and while the run is
// still executing its first node an admin edit advances the last node's
// profile. The edited model must serve the NEXT run only; this run's third node
// reaches the provider on the model it started with.
func TestAcceptance_09_PinnedRunKeepsTriggerModels(t *testing.T) {
	transport := newFakeTransport(true)
	h := newHarness(t, pinnedChainBinding(transport, pinnedChainProfiles()...))
	transport.onRequest = func(n int) {
		if n == 1 {
			h.editProfileModel(t, "summarizer", "gpt-6.1-luna")
		}
	}

	res := h.runExample(t, map[string]any{"topic": "pins"})
	require.Equal(t, instance.StateCompleted, res.instance.State)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, res.instance.ModelPinsVersion)
	assert.Equal(t, map[string]string{
		"drafter": "deepseek-v4-flash", "reviewer": "claude-sonnet-4-5", "summarizer": "gpt-6.1-sol",
	}, res.instance.ModelPins, "the trigger pinned every alias the workflow uses")

	reqs := transport.recorded()
	require.Len(t, reqs, 3)
	models := map[string]string{}
	for _, r := range reqs {
		switch {
		case strings.Contains(r.URL, "api.openai.example"):
			models["summarizer"] = r.Model
		case strings.Contains(r.URL, "api.anthropic.example"):
			models["reviewer"] = r.Model
		case strings.Contains(r.URL, "ollama.example"):
			models["drafter"] = r.Model
		}
	}
	assert.Equal(t, "gpt-6.1-sol", models["summarizer"], "the edit made mid-run must not reach the in-flight run's sink")

	// A fresh run re-pins from the edited profile.
	second := h.runExample(t, map[string]any{"topic": "pins again"})
	require.Equal(t, instance.StateCompleted, second.instance.State)
	assert.Equal(t, "gpt-6.1-luna", second.instance.ModelPins["summarizer"])
	assert.Equal(t, "gpt-6.1-luna", transport.recorded()[5].Model, "the new run executes on the edited model")
}

// TestAcceptance_09_TriggerRefusesWithoutSnapshot proves a missing snapshot
// input fails the trigger: no instance is created and no model is called.
func TestAcceptance_09_TriggerRefusesWithoutSnapshot(t *testing.T) {
	transport := newFakeTransport(true)
	missingSummarizer := pinnedChainProfiles()[:2]
	h := newHarness(t, pinnedChainBinding(transport, missingSummarizer...))

	_, _, err := h.engine.Trigger(context.Background(), h.binding.exampleName(), map[string]any{"topic": "x"}, "normal")
	require.Error(t, err, "a workflow alias with no profile cannot be pinned")

	list, err := h.instances.List(context.Background(), instance.ListFilters{})
	require.NoError(t, err)
	assert.Empty(t, list, "a refused trigger creates no instance")
	assert.Empty(t, transport.recorded(), "a refused trigger calls no model")
}
