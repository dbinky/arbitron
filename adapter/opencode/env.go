// Package opencode implements the engine.AgentProvider port by driving the
// `opencode` CLI in single-shot headless mode (`opencode -p <prompt> -c <cwd>`).
//
// IMPORTANT — installed-binary reality (opencode v0.0.55, the older Go variant):
// this build has NO `run` subcommand, NO `--model`/provider-routing flag, NO
// `--resume`/`--session`, and NO `serve`. Its real headless contract is
//
//	opencode -p "<prompt>" -c <cwd> [-q] [-f text|json]
//
// with auth read from the process env (Anthropic via ANTHROPIC_API_KEY). The
// adapter is therefore a SINGLE-SHOT mission-per-step backend (no SessionID
// threading) per the spike finding's spec §9 fallback. See env_providers.go for
// the documented multi-provider limitation.
package opencode

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// opencodeEnv returns procenv.BaseEnv() with the provider-keyed API key and
// base-url variables appended for the opencode subprocess. The key is fetched
// from binding.RevealKey(ctx) at call time and placed ONLY in the returned
// child env; it is never logged or written to a file. Injected var names that
// would shadow the base allowlist are rejected.
//
// On RevealKey error or an unknown provider, NO env is produced (nil is
// returned alongside the error) so the caller cannot launch with a partial env.
func opencodeEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	keyVar, baseURLVar, ok := providerEnvVars(binding.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, binding.Provider)
	}

	// SECURITY: a provider mapping that names a base-allowlist var (PATH, HOME,
	// ...) could shadow / spoof the controlled base env. Reject before touching
	// the secret. Mirrors adapter/process/env.go's node-env collision guard.
	if err := rejectBaseNames(keyVar, baseURLVar); err != nil {
		return nil, err
	}

	// Reveal the key LAST, only after validation, so a rejected/unknown
	// provider never even decrypts the secret. The revealed value flows only
	// into the returned slice below — never into a log line.
	if binding.RevealKey == nil {
		return nil, fmt.Errorf("opencode: binding has no RevealKey — a connection is required")
	}
	key, err := binding.RevealKey(ctx)
	if err != nil {
		return nil, err
	}

	env := procenv.BaseEnv()
	env = append(env, keyVar+"="+key)
	// An empty BaseURL omits the var entirely so opencode falls back to its own
	// default endpoint rather than seeing an empty override.
	if binding.BaseURL != "" {
		env = append(env, baseURLVar+"="+binding.BaseURL)
	}
	return env, nil
}

// rejectBaseNames returns an error if any non-empty injected var name collides
// with the procenv base allowlist (which would shadow the controlled base env).
func rejectBaseNames(names ...string) error {
	for _, n := range names {
		if n == "" {
			continue
		}
		if procenv.IsBaseName(n) {
			return fmt.Errorf("opencode: injected env var %q shadows the base allowlist", n)
		}
	}
	return nil
}
