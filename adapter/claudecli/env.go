package claudecli

import (
	"context"
	"fmt"

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
// CALL. It mirrors opencode.opencodeEnv's safety contract:
//
//   - start from procenv.BaseEnv();
//   - if binding.BaseURL != "" → append ANTHROPIC_BASE_URL=<baseURL>;
//   - if binding.RevealKey != nil → reveal at call time; on a non-empty key
//     append ANTHROPIC_API_KEY=<key>; on a RevealKey error return (nil, err) so
//     the caller never launches with a partial env (fail-closed);
//   - an empty BaseURL AND an empty/absent key → return procenv.BaseEnv()
//     unchanged (the subscription path, byte-for-byte identical to before).
//
// SECURITY: the key is fetched only here, placed ONLY in the returned child env,
// and never logged or written to argv. ANTHROPIC_API_KEY is not allowlisted, so
// it cannot be shadowed by an inherited engine value; we defensively reject a
// base-name collision on the key var (the base-url var is intentionally allowed
// in the base set, so it is NOT part of the collision guard).
func agentEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	// Defensive: the injected KEY var must not collide with the base allowlist
	// (it is deliberately absent there; this guards against a future allowlist
	// change silently shadowing the per-call key).
	if procenv.IsBaseName(anthropicKeyVar) {
		return nil, fmt.Errorf("claudecli: injected env var %q shadows the base allowlist", anthropicKeyVar)
	}

	// Reveal the key first (when a connection is present) so a failure aborts
	// BEFORE any env is assembled — no partial-env launch.
	var key string
	if binding.RevealKey != nil {
		k, err := binding.RevealKey(ctx)
		if err != nil {
			return nil, err
		}
		key = k
	}

	// Subscription path: no endpoint and no key → the base env, unchanged.
	if binding.BaseURL == "" && key == "" {
		return procenv.BaseEnv(), nil
	}

	env := procenv.BaseEnv()
	if binding.BaseURL != "" {
		env = append(env, anthropicBaseURLVar+"="+binding.BaseURL)
	}
	if key != "" {
		env = append(env, anthropicKeyVar+"="+key)
	}
	return env, nil
}
