package pi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrUnknownProvider is returned when a binding names a provider this adapter
// has no credential descriptor for. It fails the mission closed: pi would
// otherwise fall back to a key from its own ~/.pi config or the ambient shell
// and run the mission on a credential arbitron never chose.
var ErrUnknownProvider = errors.New("pi: unknown provider")

// providerOpenAICodex is pi's provider id for the ChatGPT Plus/Pro subscription
// (`pi` → /login → ChatGPT). It is the one subscription route that is both
// officially endorsed by OpenAI (the Codex for OSS programme) and billed
// against the plan allowance rather than metered separately — which is why it
// earns a row here while Claude Pro/Max does not (see below).
const providerOpenAICodex = "openai-codex"

// providerDescriptor says how one provider authenticates.
//
// Exactly one of the two shapes applies: a key-based provider names the
// environment variable pi reads its secret from, and an OAuth provider names
// nothing because pi resolves the credential from its own store. The
// descriptor-table test enforces that a row never claims both.
type providerDescriptor struct {
	// credEnvVar is the env var carrying the revealed key. Empty for OAuth rows.
	credEnvVar string
	// oauth marks a provider whose credential lives in pi's own auth.json
	// (~/.pi/agent/auth.json, written by `pi` → /login). arbitron holds no
	// secret for these, injects nothing, and never calls RevealKey.
	oauth bool
}

// providerDescriptors maps an arbitron canonical provider id onto how pi
// authenticates it.
//
// VERIFIED against pi 0.87.0 docs/providers.md. Deliberately a SHORT allowlist
// rather than pi's full 30-provider roster: a row is a promise that arbitron can
// route, authenticate and bill that provider, so one is added only when it is
// actually exercised.
//
// Deliberately absent:
//   - Claude Pro/Max, though pi supports it. Anthropic bills third-party harness
//     usage per token from the account's "extra usage" balance rather than
//     against the Claude plan limits, so routing Claude through pi converts
//     usage that is free at the margin into metered spend. Claude work belongs
//     on the claude-code backend; this omission is an economic decision, not a
//     technical gap.
//   - Bedrock and the other AWS-quartet providers, which need a credential
//     quartet rather than a single key (see opencode's bedrockEnv).
var providerDescriptors = map[string]providerDescriptor{
	provider.CanonicalAnthropicAPI: {credEnvVar: "ANTHROPIC_API_KEY"},
	"openai":                       {credEnvVar: "OPENAI_API_KEY"},
	"openrouter":                   {credEnvVar: "OPENROUTER_API_KEY"},
	"deepseek":                     {credEnvVar: "DEEPSEEK_API_KEY"},
	"google":                       {credEnvVar: "GEMINI_API_KEY"},
	"groq":                         {credEnvVar: "GROQ_API_KEY"},
	"cerebras":                     {credEnvVar: "CEREBRAS_API_KEY"},
	"mistral":                      {credEnvVar: "MISTRAL_API_KEY"},
	providerOpenAICodex:            {oauth: true},
}

// noConnectionProvider is the provider a binding carries when its profile
// references NO connection. The resolver stamps the local-Claude-subscription
// id there regardless of backend, so for a pi binding it means only "this
// profile named no connection" — never a real pi provider.
const noConnectionProvider = "claude-cli"

// resolveDescriptor picks the credential descriptor for a binding.
//
// A profile can only name a provider by referencing a CONNECTION, and every
// connection requires an api_key and a base_url. A subscription has neither, so
// a pi profile on the subscription path arrives carrying noConnectionProvider —
// which says nothing about which vendor pi should call.
//
// pi's own model syntax already answers that: "openai-codex/gpt-5.6-sol" names
// its provider in the prefix, and pi routes on exactly that. So when (and only
// when) the binding names no connection, fall back to the model's prefix.
//
// That fallback is restricted to OAUTH providers. A key-based provider reached
// this way has no connection and therefore no key for arbitron to inject;
// allowing it would silently hand the mission to whatever credential happens to
// sit in pi's own store or the ambient shell. It fails closed instead.
//
// An explicit connection-backed provider always wins, so a real connection is
// never overridden by whatever the model string happens to say.
func resolveDescriptor(binding provider.ResolvedBinding) (providerDescriptor, bool) {
	if d, ok := providerDescriptors[binding.Provider]; ok {
		return d, true
	}
	if binding.Provider != noConnectionProvider {
		return providerDescriptor{}, false
	}
	prefix, _, found := strings.Cut(binding.Model, "/")
	if !found || prefix == "" {
		return providerDescriptor{}, false
	}
	d, ok := providerDescriptors[prefix]
	if !ok || !d.oauth {
		return providerDescriptor{}, false
	}
	return d, true
}

// piEnv returns the controlled subprocess environment for one pi launch:
// procenv.BaseEnv() (PATH-augmented, allowlist-filtered, so the engine's own
// secrets are stripped) plus exactly ONE provider credential variable, fetched
// from binding.RevealKey at call time.
//
// SECURITY: the secret reaches the child ONLY through this env. It is never
// placed in argv — pi accepts `--api-key`, which this adapter deliberately does
// not use because argv is world-readable via ps — and never logged. Because
// BaseEnv strips non-allowlisted variables, a mission for one vendor cannot
// carry another vendor's ambient key into the subprocess.
//
// On any error (unknown provider, RevealKey failure, empty key, base-name
// collision) NO env is produced, so the caller cannot launch with a partial or
// fallback credential.
func piEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	d, ok := resolveDescriptor(binding)
	if !ok {
		return nil, fmt.Errorf("%w: %q (model %q)", ErrUnknownProvider, binding.Provider, binding.Model)
	}
	// OAuth/subscription providers carry no injectable secret: pi reads the
	// token from its own auth.json, which its resolution order already ranks
	// ABOVE environment variables. Injecting nothing is therefore both correct
	// and necessary — arbitron holds no key to reveal, so demanding one would
	// fail a mission pi can serve perfectly well.
	if d.oauth {
		return procenv.BaseEnv(), nil
	}
	return procenv.CredentialEnv(ctx, procenv.CredentialSpec{
		Runner:   fmt.Sprintf("pi: provider %q", binding.Provider),
		KeyVar:   d.credEnvVar,
		Reveal:   binding.RevealKey,
		EmptyKey: procenv.EmptyKeyReject,
	})
}
