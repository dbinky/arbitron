package opencode

import "errors"

// ErrUnknownProvider is returned when a ResolvedBinding names a provider that
// has no opencode env mapping. Adding a provider is a one-line entry in the
// providerEnvVarTable below.
var ErrUnknownProvider = errors.New("opencode: no env mapping for provider")

// providerEnvEntry describes a provider's opencode subprocess env contract.
//   - keyVar/baseURLVar: the env var names carrying the API key and optional
//     base-url override (single-key providers).
//   - aws: when true, the provider authenticates via the AWS credential quartet
//     (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / optional AWS_SESSION_TOKEN /
//     AWS_REGION) instead of a single key, and keyVar/baseURLVar are unused.
type providerEnvEntry struct {
	keyVar     string
	baseURLVar string
	aws        bool
}

// providerEnvVarTable is the single source of truth for opencode's per-provider
// subprocess env contract. Keys are opencode provider ids (the output of
// mapProviderID), so the table and the `-m` flag share one vocabulary.
//
// Local vs cloud Ollama: both are the same opencode provider id "ollama". The
// cloud connection carries a key → the single-key path sets OLLAMA_API_KEY (+
// base-url). The LOCAL connection is keyless: its binding has a BaseURL and a
// nil/empty RevealKey, so opencodeEnv sets only OLLAMA_BASE_URL. Keyless is a
// per-binding property (detected at reveal time, restricted to the ollama
// provider), not a table flag.
var providerEnvVarTable = map[string]providerEnvEntry{
	"anthropic":      {keyVar: "ANTHROPIC_API_KEY", baseURLVar: "ANTHROPIC_BASE_URL"},
	"openai":         {keyVar: "OPENAI_API_KEY", baseURLVar: "OPENAI_BASE_URL"},
	"openrouter":     {keyVar: "OPENROUTER_API_KEY", baseURLVar: "OPENROUTER_BASE_URL"},
	"azure":          {keyVar: "AZURE_OPENAI_API_KEY", baseURLVar: "AZURE_OPENAI_ENDPOINT"},
	"ollama":         {keyVar: "OLLAMA_API_KEY", baseURLVar: "OLLAMA_BASE_URL"},
	"amazon-bedrock": {aws: true},
}

// providerEnvVars resolves the key and base-url environment variable names for
// a provider. ok is false for an unknown (or empty) provider. AWS providers
// return ok=true with empty key/base-url names (the caller routes them to the
// AWS-quartet builder via providerEnvEntryFor).
func providerEnvVars(provider string) (keyVar, baseURLVar string, ok bool) {
	v, ok := providerEnvVarTable[provider]
	if !ok {
		return "", "", false
	}
	return v.keyVar, v.baseURLVar, true
}

// providerEnvEntryFor returns the full entry (including the aws flag) for a
// provider; ok is false for an unknown/empty provider.
func providerEnvEntryFor(provider string) (providerEnvEntry, bool) {
	v, ok := providerEnvVarTable[provider]
	return v, ok
}
