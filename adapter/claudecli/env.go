package claudecli

import (
	"context"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// anthropicKeyVar is the per-call-injected Anthropic credential var. It is
// deliberately NOT on the procenv base allowlist (the engine's own key must
// never leak into an agent subprocess); only the value revealed for THIS call
// is placed in the child env. anthropicBaseURLVar IS on the allowlist (a
// non-secret endpoint), so the collision guard targets only the key var.
const (
	anthropicKeyVar     = "ANTHROPIC_API_KEY"
	anthropicBaseURLVar = "ANTHROPIC_BASE_URL"
)

// agentEnv builds the claude-cli agent subprocess env from a ResolvedBinding,
// injecting the org-gateway (or any configured provider) endpoint + key PER
// CALL. The fail-closed skeleton (reveal-first, base-name collision rejection,
// strip-then-append the base-url var, partial-env-NOT-emitted on reveal error)
// lives once in procenv.CredentialEnv so claudecli and opencode cannot drift;
// only the claude-cli-specific bits are set here.
//
// Empty-key policy is EmptyKeyAllowBaseURLOnly with NO BaseURL requirement: an
// empty BaseURL AND an empty/absent key yields procenv.BaseEnv() unchanged (the
// subscription path), and an empty key with a BaseURL is a base-url-only launch.
//
// SECURITY: the key is fetched only at launch time, placed ONLY in the returned
// child env, and never logged or written to argv. ANTHROPIC_API_KEY is not
// allowlisted, so CredentialEnv's collision guard defends against a future
// allowlist change silently shadowing the per-call key.
func agentEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	return procenv.CredentialEnv(ctx, procenv.CredentialSpec{
		Runner:     "claudecli",
		KeyVar:     anthropicKeyVar,
		BaseURLVar: anthropicBaseURLVar,
		BaseURL:    binding.BaseURL,
		Reveal:     binding.RevealKey,
		EmptyKey:   procenv.EmptyKeyAllowBaseURLOnly,
	})
}

// agentEnvWithRules builds the credential env (agentEnv) and then appends the
// capability-derived env (context window + effort) from the binding via the
// capability rules (profile-editing Phase 3). It is the production env path for
// the agent run: the credential vars come first (fail-closed on a reveal error,
// no partial env), then the non-secret policy knobs.
//
// agentEnv is kept intact (credential-only) so its existing security tests still
// pin the credential skeleton; this wrapper is what the adapter calls so a
// profile's 200k/effort axes actually reach the subprocess.
func agentEnvWithRules(ctx context.Context, binding provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	if provider.IsLunaRoute(binding.Provider) {
		env, err := lunaRouteAgentEnv(ctx, binding, rules)
		return env, provider.LocalLunaRouteError("environment", err)
	}
	env, err := agentEnv(ctx, binding)
	if err != nil {
		return nil, err
	}
	return applyCapabilityEnv(env, binding, rules)
}
