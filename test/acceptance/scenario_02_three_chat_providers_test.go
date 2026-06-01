package acceptance

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
)

// TestAcceptance_02_AllThreeChatProviders proves spec §8 #2: switching the
// assessor (an `llm_call`) from a Claude profile to an Azure AI Foundry or
// OpenRouter profile is a SINGLE profile-reference change, and the node runs
// against the correct chat backend. Claude, Azure, and OpenRouter all function
// as chat backends.
//
// Requires: Phase 1 claude-cli chat; Phase 3 Azure AI Foundry + OpenRouter
// native ChatProvider; Phase 2 resolution + RevealKey.
//
// The three runs differ ONLY in the assessor profile reference (and the
// connection it points at); the editor profile, the graph, and the templates are
// identical. Each run is asserted to drive a DIFFERENT chat adapter — claude-cli
// (subprocess), Azure HTTP, OpenRouter HTTP — via that adapter's fake seam.
func TestAcceptance_02_AllThreeChatProviders(t *testing.T) {
	// The editor profile is held constant across all three runs (claude agent on
	// the no-key path); only the assessor profile reference changes.
	const editorAlias = "editor"
	editorProfile := provider.Profile{Alias: editorAlias, Kind: provider.ProfileKindAgent, ConnectionRef: "", Model: "claude-sonnet-4"}

	t.Run("claude chat backend", func(t *testing.T) {
		runner := newFakeRunner()
		runner.on("--dangerously-skip-permissions", claudeAgentLines...)
		runner.withFallback(claudeAssessorLines(false)...)

		b := binding{
			name:     "assessor=claude",
			profiles: []provider.Profile{editorProfile, {Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "", Model: "claude-sonnet-4"}},
			runner:   runner,
		}
		h := newHarness(t, b)

		// claude chat cannot self-terminate (raw-text structured output); bound it.
		const guard = 4
		idCh := make(chan uuid.UUID, 1)
		runner.armGuard(guard, func() { _ = h.engine.Cancel(<-idCh) })
		id := h.trigger(t, 2)
		idCh <- id
		inst := h.waitForTerminal(t, id, 10*time.Second)
		require.Equal(t, instance.StateCancelled, inst.State)

		// The assessor ran the tool-suppressed claude-cli chat adapter.
		var sawChat bool
		for _, s := range runner.recordedSpecs() {
			joined := strings.Join(s.Args, " ")
			if strings.Contains(joined, "--dangerously-skip-permissions") {
				continue
			}
			if v, ok := argValue(s.Args, "--allowedTools"); ok && v == "" {
				sawChat = true
			}
		}
		assert.True(t, sawChat, "assessor must route to the claude-cli chat adapter")
	})

	t.Run("openrouter chat backend", func(t *testing.T) {
		runner := newFakeRunner().on("--dangerously-skip-permissions", claudeAgentLines...)
		transport := newFakeTransport(true) // assessor reports done → loop exits
		b := binding{
			name:     "assessor=openrouter",
			profiles: []provider.Profile{editorProfile, {Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "openrouter-chat", Model: "deepseek/deepseek-r1"}},
			connections: []seedConn{{
				conn: provider.Connection{Name: "openrouter-chat", Provider: "openrouter", BaseURL: "https://openrouter.example/api/v1"},
				key:  openrouterKey,
			}},
			runner:    runner,
			transport: transport,
		}
		h := newHarness(t, b)

		res := h.runWorkedExample(t, 5)
		require.Equal(t, instance.StateCompleted, res.instance.State,
			"openrouter assessor completes the loop on assessor.done")

		reqs := transport.recorded()
		require.NotEmpty(t, reqs, "assessor must route to the OpenRouter HTTP adapter")
		assert.Contains(t, reqs[0].URL, "openrouter.example/api/v1/chat/completions")
		assert.Equal(t, "Bearer "+openrouterKey, reqs[0].Auth, "OpenRouter uses a Bearer key")
		assert.Empty(t, reqs[0].APIKey, "OpenRouter does not use the Azure api-key header")
	})

	// Azure (like claude-cli) returns structured output as RAW TEXT rather than a
	// parsed map, so `assessor.done` does not resolve and the loop cannot
	// self-terminate over the Azure chat path. As with the claude subtest we
	// bound the run with a fake-runner guard and assert the PATH: the Azure HTTP
	// adapter is driven against the correct deployment endpoint with the correct
	// api-key. (OpenRouter is the chat provider whose native mapOutput parses the
	// structured result into a map, so only it self-terminates above.)
	t.Run("azure foundry chat backend", func(t *testing.T) {
		const azureKey = "sk-SENTINEL-azure-DO-NOT-LOG"
		runner := newFakeRunner().on("--dangerously-skip-permissions", claudeAgentLines...)
		transport := newFakeTransport(false)
		b := binding{
			name:     "assessor=azure",
			profiles: []provider.Profile{editorProfile, {Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "azure-chat", Model: "gpt-4o-deployment"}},
			connections: []seedConn{{
				conn: provider.Connection{
					Name: "azure-chat", Provider: "azure-openai",
					BaseURL: "https://eastus.example.azure.com",
					Extras:  map[string]string{"api-version": "2024-02-01"},
				},
				key: azureKey,
			}},
			runner:    runner,
			transport: transport,
		}
		h := newHarness(t, b)

		const guard = 4
		idCh := make(chan uuid.UUID, 1)
		runner.armGuard(guard, func() { _ = h.engine.Cancel(<-idCh) })
		id := h.trigger(t, 5)
		idCh <- id
		inst := h.waitForTerminal(t, id, 10*time.Second)
		require.Equal(t, instance.StateCancelled, inst.State)

		reqs := transport.recorded()
		require.NotEmpty(t, reqs, "assessor must route to the Azure HTTP adapter")
		assert.Contains(t, reqs[0].URL, "eastus.example.azure.com")
		assert.Contains(t, reqs[0].URL, "/openai/deployments/gpt-4o-deployment/chat/completions")
		assert.Contains(t, reqs[0].URL, "api-version=2024-02-01")
		assert.Equal(t, azureKey, reqs[0].APIKey, "Azure uses the api-key header")
		assert.Empty(t, reqs[0].Auth, "Azure does not use the Authorization Bearer header")
	})
}
