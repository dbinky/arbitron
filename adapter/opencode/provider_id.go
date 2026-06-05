package opencode

import "strings"

// providerIDMap maps arbitron provider strings (as they appear on
// ResolvedBinding.Provider) to opencode's provider ids used in
// `run -m <provider>/<model>`. It is the single source of truth shared by
// args.go (the -m flag) and env_providers.go (the auth env table): the env table
// is keyed by these opencode ids, so both share one vocabulary.
//
// Note: "claude-cli" is intentionally ABSENT — the Factory never routes a
// claude-cli agent binding to opencode (it uses adapter/claudecli), so a
// claude-cli value here is an unknown provider and must fail closed.
var providerIDMap = map[string]string{
	"anthropic":      "anthropic",
	"anthropic-api":  "anthropic",
	"openai":         "openai",
	"openrouter":     "openrouter",
	"ollama":         "ollama",
	"bedrock":        "amazon-bedrock",
	"amazon-bedrock": "amazon-bedrock",
	"azure":          "azure",
	"azure-openai":   "azure",
	"azure-foundry":  "azure",
}

// mapProviderID normalizes (trim + lowercase) and maps an arbitron provider
// string to its opencode provider id. ok is false for an unknown or empty
// provider so callers can fail closed.
func mapProviderID(arbitronProvider string) (opencodeID string, ok bool) {
	id, ok := providerIDMap[strings.ToLower(strings.TrimSpace(arbitronProvider))]
	return id, ok
}
