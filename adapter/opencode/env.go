// Package opencode implements engine.AgentProvider by driving the modern
// `opencode run <prompt> -m <provider>/<model>` contract with per-provider auth
// injected via the subprocess env (never argv/logs) and an open-by-default
// permission posture. See args.go (run contract), posture.go (permissions),
// providers.go (descriptor table), and version.go (binary version probe).
package opencode

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrBedrockConfig is returned when a bedrock binding lacks the required
// non-secret Extras (region, access-key-id). It is a configuration-class error
// raised BEFORE any secret is revealed.
var ErrBedrockConfig = errors.New("opencode: bedrock requires region and access-key-id in Extras")

// opencodeEnv returns procenv.BaseEnv() with the provider credential variable
// appended for the opencode subprocess. The secret is fetched from
// binding.RevealKey(ctx) at call time and placed ONLY in the returned child env;
// it is never logged or written to a file.
//
// opencode 1.17.x ignores *_BASE_URL env vars on its routing path — base URLs
// are delivered via a generated OPENCODE_CONFIG_CONTENT JSON in a later task.
// This function therefore sets ONLY the credential env var, never a base-URL var.
//
// On any error (unknown provider, missing bedrock config, RevealKey failure,
// base-name collision) NO env is produced (nil is returned alongside the error)
// so the caller cannot launch with a partial env.
func opencodeEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	d, ok := descriptorFor(binding.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, binding.Provider)
	}

	// AWS-quartet providers (bedrock) take a dedicated builder.
	if d.aws {
		return bedrockEnv(ctx, binding)
	}

	// Keyless providers with no credential env var (local ollama) need only the
	// base env — no key to inject, no base-URL var (that moves to config).
	if d.credEnvVar == "" && d.keyless {
		return procenv.BaseEnv(), nil
	}

	// All other providers: inject only the credential key var.
	// BaseURLVar and BaseURL are intentionally empty — opencode 1.17.x does not
	// honour *_BASE_URL on its routing path; the base URL will be in config.
	emptyKey := procenv.EmptyKeyReject
	if d.keyless {
		emptyKey = procenv.EmptyKeyAllowBaseURLOnly
	}
	return procenv.CredentialEnv(ctx, procenv.CredentialSpec{
		Runner:     fmt.Sprintf("opencode: provider %q", binding.Provider),
		KeyVar:     d.credEnvVar,
		BaseURLVar: "",
		BaseURL:    "",
		Reveal:     binding.RevealKey,
		EmptyKey:   emptyKey,
	})
}

// bedrockEnv builds the AWS credential quartet for a bedrock binding. region and
// access-key-id come from non-secret Extras; the secret-access-key (and optional
// session token) come from the sealed RevealKey payload, parsed by the SHARED
// awssig.ParseCredential (the SAME parser Phase 3's bedrock chat adapter uses).
func bedrockEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	region := binding.Extras["region"]
	accessKeyID := binding.Extras["access-key-id"]
	if region == "" || accessKeyID == "" {
		return nil, ErrBedrockConfig
	}
	// SECURITY: reject any base-name collision before revealing.
	if err := rejectBaseNames("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION"); err != nil {
		return nil, err
	}
	if binding.RevealKey == nil {
		return nil, fmt.Errorf("opencode: bedrock binding has no RevealKey — a connection is required")
	}
	sealed, err := binding.RevealKey(ctx)
	if err != nil {
		return nil, err
	}
	// SHARED parser: tolerant bare-string-or-JSON composite (adapter/awssig).
	cred, err := awssig.ParseCredential(sealed)
	if err != nil {
		// awssig.ErrMissingSecret (empty/empty-secret) maps to a config-class error.
		return nil, fmt.Errorf("opencode: bedrock secret-access-key: %w", err)
	}

	env := procenv.BaseEnv()
	env = append(env,
		"AWS_ACCESS_KEY_ID="+accessKeyID,
		"AWS_SECRET_ACCESS_KEY="+cred.SecretAccessKey,
		"AWS_REGION="+region,
	)
	if cred.SessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+cred.SessionToken)
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
