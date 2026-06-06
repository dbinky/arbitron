package acceptance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
)

// TestAcceptance_08_MixedChatProviders proves spec §Success-Criteria #1: a
// workflow whose nodes use DIFFERENT chat providers runs end-to-end. The
// committed `mixed-chat-providers` example is a linear chain of three
// tool-suppressed chat (llm_call) nodes:
//
//	drafter  → ollama        (local, keyless OpenAI-compatible → adapter/openrouter)
//	reviewer → anthropic-api (keyed Messages API            → adapter/anthropicapi)
//	summarizer → openai      (keyed OpenAI-compatible        → adapter/openrouter)
//
// Each node is bound to its own provider via its own profile alias; the run is
// asserted to drive each provider's distinct endpoint with the correct auth
// header (keyed providers send their key; the keyless local-Ollama node sends
// none), and the instance completes.
func TestAcceptance_08_MixedChatProviders(t *testing.T) {
	const anthropicKey = "sk-SENTINEL-anthropic-DO-NOT-LOG"
	const openaiKey = "sk-SENTINEL-openai-DO-NOT-LOG"

	runner := newFakeRunner() // no claude-cli node in this example; present for wiring symmetry
	transport := newFakeTransport(true)

	b := binding{
		name:         "mixed-chat-providers",
		workflowName: "mixed-chat-providers",
		profiles: []provider.Profile{
			{Alias: "drafter", Kind: provider.ProfileKindChat, ConnectionRef: "ollama-local", Model: "llama3"},
			{Alias: "reviewer", Kind: provider.ProfileKindChat, ConnectionRef: "anthropic-chat", Model: "claude-3-5-sonnet"},
			{Alias: "summarizer", Kind: provider.ProfileKindChat, ConnectionRef: "openai-chat", Model: "gpt-4o"},
		},
		connections: []seedConn{
			{
				// Local Ollama is keyless: an empty key seals a keyless connection
				// (no Authorization header is sent by the OpenAI-compatible adapter).
				conn: provider.Connection{Name: "ollama-local", Provider: "ollama", BaseURL: "http://localhost:11434/v1"},
				key:  "",
			},
			{
				conn: provider.Connection{Name: "anthropic-chat", Provider: "anthropic-api", BaseURL: "https://api.anthropic.example"},
				key:  anthropicKey,
			},
			{
				conn: provider.Connection{Name: "openai-chat", Provider: "openai", BaseURL: "https://api.openai.example/v1"},
				key:  openaiKey,
			},
		},
		runner:    runner,
		transport: transport,
	}
	h := newHarness(t, b)

	res := h.runExample(t, map[string]any{"topic": "vendor-neutral provider abstraction"})
	require.Equal(t, instance.StateCompleted, res.instance.State,
		"the mixed-provider chain runs all three chat nodes to completion")

	reqs := transport.recorded()
	require.Len(t, reqs, 3, "exactly one HTTP request per chat node (drafter, reviewer, summarizer)")

	// Index the recorded requests by their endpoint so the assertions are
	// order-independent (the engine walks drafter→reviewer→summarizer, but we
	// assert on identity, not arrival order).
	var ollamaReq, anthropicReq, openaiReq *recordedRequest
	for i := range reqs {
		switch {
		case strings.Contains(reqs[i].URL, "localhost:11434"):
			ollamaReq = &reqs[i]
		case strings.Contains(reqs[i].URL, "api.anthropic.example"):
			anthropicReq = &reqs[i]
		case strings.Contains(reqs[i].URL, "api.openai.example"):
			openaiReq = &reqs[i]
		}
	}

	// Drafter → local Ollama (OpenAI-compatible /chat/completions), keyless: no
	// Authorization header is sent.
	require.NotNil(t, ollamaReq, "drafter must hit the local-Ollama endpoint")
	assert.Contains(t, ollamaReq.URL, "localhost:11434/v1/chat/completions")
	assert.Empty(t, ollamaReq.Auth, "keyless local Ollama sends no Authorization header")
	assert.Empty(t, ollamaReq.APIKey)

	// Reviewer → Anthropic Messages API (/v1/messages) with the x-api-key header.
	require.NotNil(t, anthropicReq, "reviewer must hit the Anthropic Messages endpoint")
	assert.Contains(t, anthropicReq.URL, "api.anthropic.example/v1/messages")
	assert.Equal(t, anthropicKey, anthropicReq.XAPIKey, "anthropic-api uses the x-api-key header")
	assert.Empty(t, anthropicReq.Auth, "anthropic-api does not use the Authorization Bearer header")

	// Summarizer → OpenAI (OpenAI-compatible /chat/completions) with a Bearer key.
	require.NotNil(t, openaiReq, "summarizer must hit the OpenAI endpoint")
	assert.Contains(t, openaiReq.URL, "api.openai.example/v1/chat/completions")
	assert.Equal(t, "Bearer "+openaiKey, openaiReq.Auth, "openai uses a Bearer key")
	assert.Empty(t, openaiReq.APIKey)

	// Cross-contamination: each provider's key must be absent from every OTHER
	// provider's request. The fakeTransport captures all headers, so a mis-wired
	// adapter that sends a key to the wrong endpoint is caught here.
	assert.Empty(t, ollamaReq.XAPIKey,
		"keyless Ollama request must not carry an x-api-key header (Anthropic leak)")
	assert.NotContains(t, ollamaReq.Auth, anthropicKey,
		"Ollama request must not carry the Anthropic key in Authorization")
	assert.NotContains(t, ollamaReq.Auth, openaiKey,
		"Ollama request must not carry the OpenAI key in Authorization")

	assert.Empty(t, anthropicReq.APIKey,
		"Anthropic request must not carry an Azure api-key header")
	assert.NotContains(t, anthropicReq.XAPIKey, openaiKey,
		"Anthropic request must not carry the OpenAI key in x-api-key")

	assert.Empty(t, openaiReq.XAPIKey,
		"OpenAI request must not carry an x-api-key header (Anthropic leak)")
	assert.NotContains(t, openaiReq.Auth, anthropicKey,
		"OpenAI request must not carry the Anthropic key in Authorization")

	// Secrecy: neither keyed secret appears in any captured log output.
	logs := h.logBuf.String()
	assert.NotContains(t, logs, anthropicKey)
	assert.NotContains(t, logs, openaiKey)
}
