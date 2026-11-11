package codex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// LunaRouteKeyVar is referenced by Codex's Responses provider definition. The
// guarded connection credential is supplied only through the child environment.
const LunaRouteKeyVar = "LUNAROUTE_API_KEY"
const lunaRouteHomePrefix = "arbitron-codex-lunaroute-"

// LR-07's provider and review selectors are implemented independently of live
// release verification. Neither argv nor the private home contains a key.
func lunaRouteExecExtra(b provider.ResolvedBinding) ([]string, error) {
	base, err := provider.LunaRouteOpenAIBaseURL(b.BaseURL)
	if err != nil {
		return nil, err
	}
	extra := []string{
		"-c", `model_provider="lunaroute"`,
		"-c", `model_providers.lunaroute.name="LunaRoute"`,
		"-c", `model_providers.lunaroute.base_url="` + base + `"`,
		"-c", `model_providers.lunaroute.env_key="` + LunaRouteKeyVar + `"`,
		"-c", `model_providers.lunaroute.wire_api="responses"`,
		"-c", `review_model="` + b.Model + `"`,
	}
	effort, err := lunaRouteEffortArgs(b)
	if err != nil {
		return nil, err
	}
	return append(extra, effort...), nil
}
func lunaRouteEffortArgs(b provider.ResolvedBinding) ([]string, error) {
	switch b.Effort {
	case "", provider.EffortDefault:
		return nil, nil
	case provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh:
		return []string{"-c", codexEffortConfigKey + "=" + string(b.Effort)}, nil
	case provider.EffortNone, provider.EffortMax:
		return nil, &provider.LunaRouteRefusal{Reason: provider.ErrLunaRouteAxisUnsupported, Model: b.Model, Backend: "codex", Detail: "set effort to default or an exactly supported codex effort"}
	default:
		return nil, fmt.Errorf("%w: unknown codex effort", provider.ErrInvalidEffort)
	}
}

type lunaRouteLaunch struct {
	env     []string
	home    string
	cleanup func()
}

func prepareLunaRouteLaunch(ctx context.Context, b provider.ResolvedBinding) (lunaRouteLaunch, error) {
	return prepareLunaRouteLaunchWithFS(ctx, b, os.MkdirTemp, os.Chmod)
}

// The filesystem seam permits deterministic creation/permission failure tests;
// the production entry always uses the OS implementation above.
func prepareLunaRouteLaunchWithFS(ctx context.Context, b provider.ResolvedBinding, mkdir func(string, string) (string, error), chmod func(string, os.FileMode) error) (lunaRouteLaunch, error) {
	b = provider.GuardLunaRouteReveal(b)
	env, err := procenv.CredentialEnv(ctx, procenv.CredentialSpec{Runner: "codex: lunaroute", KeyVar: LunaRouteKeyVar, Reveal: b.RevealKey, EmptyKey: procenv.EmptyKeyReject})
	if err != nil {
		return lunaRouteLaunch{}, provider.LocalLunaRouteError("environment", err)
	}
	home, err := mkdir("", lunaRouteHomePrefix)
	if err != nil {
		return lunaRouteLaunch{}, provider.LocalLunaRouteError("private home", err)
	}
	cleanup := func() { _ = os.RemoveAll(home) }
	if err := chmod(home, 0700); err != nil {
		cleanup()
		return lunaRouteLaunch{}, provider.LocalLunaRouteError("private home permissions", err)
	}
	for _, name := range []string{codexHomeVar, "OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		env = procenv.RemoveVar(env, name)
	}
	env = append(env, codexHomeVar+"="+home)
	return lunaRouteLaunch{env: env, home: home, cleanup: cleanup}, nil
}

// Reuse the existing compatibility floor, but never forward the operator's
// subscription home or the inference credential to the version subprocess.
func (c *core) ensureLunaRouteCompatible(ctx context.Context, env []string) error {
	c.probeOnce.Do(func() {
		c.version, c.probeErr = compatibleVersion(ctx, c.runner, c.binPath, procenv.RemoveVar(env, LunaRouteKeyVar))
	})
	return provider.LocalLunaRouteError("version", c.probeErr)
}

func safeLunaRouteIdentity(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\"\\") && strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0 && provider.RedactLunaRoute(s) == s
}
