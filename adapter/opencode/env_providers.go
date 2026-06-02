package opencode

import "errors"

// ErrUnknownProvider is returned when a ResolvedBinding names a provider that
// has no opencode env mapping. Adding a provider is a one-line entry in the
// providerEnvVarTable below.
var ErrUnknownProvider = errors.New("opencode: no env mapping for provider")

// providerEnvVarTable is the single source of truth for opencode's per-provider
// subprocess env contract: which environment variables carry the API key and
// (optional) base-url override for each provider.
//
// LIMITATION (opencode v0.0.55, the installed Go variant): this binary has NO
// `--model` / provider-routing flag. Headless model selection is implicit and
// driven entirely by which provider key is present in the env — and in
// practice this build authenticates Anthropic via ANTHROPIC_API_KEY. The
// non-anthropic entries below are forward-looking: they encode the documented
// per-provider env names so that a future opencode build with explicit
// provider routing can be swapped in WITHOUT touching the adapter. On the
// installed binary, open-weight / non-Anthropic routing is effectively limited
// to whatever the binary auto-selects from the injected key.
var providerEnvVarTable = map[string]struct {
	keyVar     string
	baseURLVar string
}{
	"anthropic":  {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
	"openai":     {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	"openrouter": {"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
	"azure":      {"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT"},
}

// providerEnvVars resolves the key and base-url environment variable names for
// a provider. ok is false for an unknown (or empty) provider.
func providerEnvVars(provider string) (keyVar, baseURLVar string, ok bool) {
	v, ok := providerEnvVarTable[provider]
	if !ok {
		return "", "", false
	}
	return v.keyVar, v.baseURLVar, true
}
