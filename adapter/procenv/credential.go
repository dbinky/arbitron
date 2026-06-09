package procenv

import (
	"context"
	"fmt"
)

// EmptyKeyPolicy decides what a revealed-but-empty (or absent) credential key
// means for a given runner. The claude-cli subscription path and the opencode
// keyless-provider path are both legitimate keyless launches; every other
// provider must fail closed on an empty key.
type EmptyKeyPolicy int

const (
	// EmptyKeyReject fails closed when the revealed key is empty: the provider
	// requires a credential and an empty one is a misconfiguration, never an
	// unauthenticated launch.
	EmptyKeyReject EmptyKeyPolicy = iota
	// EmptyKeyAllowBaseURLOnly treats an empty key as a base-URL-only launch
	// (the var is simply omitted). Used by keyless/self-hosted and subscription
	// paths where running without a key is intentional.
	EmptyKeyAllowBaseURLOnly
)

// CredentialSpec parameterizes the shared fail-closed credential-injection
// skeleton (see CredentialEnv). It captures the parts that vary between the
// claude-cli and opencode runners; the security-relevant control flow
// (reveal-first, base-name collision rejection, strip-then-append the base-URL
// var, partial-env-NOT-emitted on error) lives once in CredentialEnv so the two
// runners cannot drift.
type CredentialSpec struct {
	// Runner names the calling adapter for error messages (e.g. "claudecli").
	Runner string
	// KeyVar is the env var that carries the secret (e.g. ANTHROPIC_API_KEY).
	// It MUST NOT be on the base allowlist; CredentialEnv defensively rejects a
	// collision so a future allowlist change cannot silently shadow the per-call
	// key. May be empty only when no key is ever injected.
	KeyVar string
	// BaseURLVar is the env var that carries the (non-secret) endpoint, e.g.
	// ANTHROPIC_BASE_URL. Empty means the runner injects no base-URL var.
	BaseURLVar string
	// ExtraGuardedVars are additional injected var names that must not collide
	// with the base allowlist (e.g. the AWS credential quartet). Empty names are
	// ignored.
	ExtraGuardedVars []string
	// BaseURL is the per-call endpoint to inject (already resolved from the
	// binding). Empty omits the base-URL var.
	BaseURL string
	// Reveal lazily decrypts the credential at launch time. Nil means "no key
	// for this call" (handled per EmptyKey). A non-nil Reveal that errors aborts
	// BEFORE any env is assembled (fail closed).
	Reveal func(ctx context.Context) (string, error)
	// EmptyKey selects how a revealed-but-empty key is treated.
	EmptyKey EmptyKeyPolicy
	// RequireBaseURLWhenKeyless rejects a keyless (empty-key) launch that also
	// has no BaseURL. Used by self-hosted providers where a keyless launch only
	// makes sense against an explicit local endpoint. Ignored when EmptyKey is
	// EmptyKeyReject.
	RequireBaseURLWhenKeyless bool
}

// CredentialEnv builds the controlled subprocess env for a credentialed runner.
// It is the SINGLE shared fail-closed skeleton behind claudecli.agentEnv and
// opencode.opencodeEnv; the per-runner specifics (empty-key policy, extra
// guarded vars, base-URL var name) are carried in spec.
//
// SECURITY contract, enforced in order:
//
//   - reject a base-name collision on KeyVar / ExtraGuardedVars (a secret var
//     must never be on the base allowlist, or it could be shadowed by an
//     inherited engine value);
//   - reveal the key FIRST (when Reveal != nil) so a reveal error aborts before
//     any env is assembled — the caller never launches with a partial env;
//   - apply EmptyKey: reject an empty key for credentialed providers, or allow a
//     base-URL-only launch for keyless/subscription paths;
//   - place the secret ONLY in the returned child env (never argv/logs), and
//     strip any operator-inherited BaseURLVar before appending the per-call one
//     so the subprocess sees exactly one entry.
func CredentialEnv(ctx context.Context, spec CredentialSpec) ([]string, error) {
	// Defensive: injected secret vars must not collide with the base allowlist.
	guarded := append([]string{spec.KeyVar}, spec.ExtraGuardedVars...)
	for _, name := range guarded {
		if name == "" {
			continue
		}
		if IsBaseName(name) {
			return nil, fmt.Errorf("%s: injected env var %q shadows the base allowlist", spec.Runner, name)
		}
	}

	// Reveal the key first so a failure aborts before any env is assembled.
	var key string
	if spec.Reveal != nil {
		k, err := spec.Reveal(ctx)
		if err != nil {
			return nil, err
		}
		key = k
	}

	if key == "" {
		switch spec.EmptyKey {
		case EmptyKeyReject:
			return nil, fmt.Errorf("%s: provider requires a key but none was produced", spec.Runner)
		case EmptyKeyAllowBaseURLOnly:
			if spec.RequireBaseURLWhenKeyless && spec.BaseURL == "" {
				return nil, fmt.Errorf("%s: keyless launch requires a BaseURL", spec.Runner)
			}
		}
	}

	env := BaseEnv()
	if spec.BaseURL != "" && spec.BaseURLVar != "" {
		// Strip any operator-inherited base-URL var (BaseEnv may pass it through
		// when it is allowlisted) before appending the authoritative per-call
		// binding value, so the subprocess sees exactly one entry.
		env = RemoveVar(env, spec.BaseURLVar)
		env = append(env, spec.BaseURLVar+"="+spec.BaseURL)
	}
	if key != "" && spec.KeyVar != "" {
		env = append(env, spec.KeyVar+"="+key)
	}
	return env, nil
}
