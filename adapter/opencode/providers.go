package opencode

import "github.com/arbitron/arbitron/domain/provider"

// providerDescriptor is the per-provider opencode 1.17.x contract: the `-m`
// prefix, the credential env var carrying the secret (empty for keyless/aws),
// the npm package + whether a config `provider` block is always required, and
// the AWS-quartet flag for bedrock. It is the single opencode-side vocabulary
// shared by args.go (`-m`), env.go (credential env), and config.go (provider block).
type providerDescriptor struct {
	opencodeID   string // `-m <opencodeID>/<model>` prefix
	credEnvVar   string // env var holding the secret key ("" => keyless or aws)
	npmPackage   string // npm package when a provider{} block is emitted
	alwaysConfig bool   // emit a provider{} block even without a custom BaseURL
	keyless      bool   // may launch without a key (local ollama)
	aws          bool   // bedrock AWS-quartet credential model
}

// providerDescriptors is keyed by the DOMAIN-CANONICAL provider id
// (provider.Canonicalize output): the alias collapse (anthropic/anthropic-api,
// bedrock/amazon-bedrock, the azure trio) lives once in Canonicalize, so this
// table lists canonical ids only. Note "ollama-cloud" is canonically DISTINCT
// from "ollama" (url_policy loopback boundary) and is listed explicitly.
var providerDescriptors = map[string]providerDescriptor{
	provider.CanonicalAnthropicAPI: {opencodeID: "anthropic", credEnvVar: "ANTHROPIC_API_KEY", npmPackage: "@ai-sdk/anthropic"},
	"openai":                       {opencodeID: "openai", credEnvVar: "OPENAI_API_KEY", npmPackage: "@ai-sdk/openai"},
	"litellm":                      {opencodeID: "litellm", credEnvVar: "OPENAI_API_KEY", npmPackage: "@ai-sdk/openai-compatible", alwaysConfig: true},
	"openrouter":                   {opencodeID: "openrouter", credEnvVar: "OPENROUTER_API_KEY", npmPackage: "@openrouter/ai-sdk-provider"},
	provider.CanonicalAzure:        {opencodeID: "azure", credEnvVar: "AZURE_API_KEY", npmPackage: "@ai-sdk/azure", alwaysConfig: true},
	provider.CanonicalBedrock:      {opencodeID: "amazon-bedrock", aws: true},
	"ollama":                       {opencodeID: "ollama", npmPackage: "@ai-sdk/openai-compatible", alwaysConfig: true, keyless: true},
	"ollama-cloud":                 {opencodeID: "ollama-cloud", credEnvVar: "OLLAMA_API_KEY"},
}

// descriptorFor canonicalizes an arbitron provider string and returns its
// opencode 1.17.x descriptor. ok is false for an unknown/empty provider so
// callers fail closed.
func descriptorFor(arbitronProvider string) (providerDescriptor, bool) {
	d, ok := providerDescriptors[provider.Canonicalize(arbitronProvider)]
	return d, ok
}
