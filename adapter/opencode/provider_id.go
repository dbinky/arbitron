package opencode

import "github.com/arbitron/arbitron/domain/provider"

// providerIDMap maps a DOMAIN-CANONICAL provider id (the output of
// provider.Canonicalize) to opencode's provider id used in
// `run -m <provider>/<model>`. It is the single source of truth shared by
// args.go (the -m flag) and env_providers.go (the auth env table): the env table
// is keyed by these opencode ids, so both share one vocabulary.
//
// The alias collapse (bedrock/amazon-bedrock, anthropic-api/anthropic, the azure
// trio) lives ONCE in provider.Canonicalize, so this map only needs the canonical
// ids — it does not re-list every spelling.
//
// Note: "claude-cli" is intentionally ABSENT — the Factory never routes a
// claude-cli agent binding to opencode (it uses adapter/claudecli), so a
// claude-cli value here is an unknown provider and must fail closed.
var providerIDMap = map[string]string{
	provider.CanonicalAnthropicAPI: "anthropic",
	"openai":                       "openai",
	// org-readiness Phase 2: a LiteLLM-family agent binding maps to the opencode
	// "openai" provider (OPENAI_API_KEY + OPENAI_BASE_URL), so opencode targets
	// LiteLLM's OpenAI-compatible surface via `-m openai/<model>`. Symmetric with
	// the chat tier, which already accepts "litellm" through the openai family.
	"litellm":                 "openai",
	"openrouter":              "openrouter",
	"ollama":                  "ollama",
	// "ollama-cloud" is an accepted ollama-family alias that Canonicalize
	// deliberately does NOT collapse to "ollama" (the domain keeps them distinct so
	// cloud Ollama can never inherit local Ollama's loopback carve-out in
	// url_policy). So, like the chat factory (adapter/provider) and probe factory
	// (adapter/probe) which both list providerOllamaCloud explicitly, this map must
	// carry the alias too — otherwise an opencode agent binding on a cloud-Ollama
	// connection fails closed at launch ("no env mapping for provider").
	"ollama-cloud": "ollama",
	provider.CanonicalBedrock: "amazon-bedrock",
	provider.CanonicalAzure:   "azure",
}

// mapProviderID canonicalizes an arbitron provider string (via the shared
// provider.Canonicalize: trim + lowercase + documented alias collapse) and maps
// the canonical id to its opencode provider id. ok is false for an unknown or
// empty provider so callers can fail closed.
func mapProviderID(arbitronProvider string) (opencodeID string, ok bool) {
	id, ok := providerIDMap[provider.Canonicalize(arbitronProvider)]
	return id, ok
}
