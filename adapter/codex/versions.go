package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
)

// HarnessFloor names a codex feature arbitron relies on and the first release
// that provides it.
type HarnessFloor struct {
	Feature string
	Min     string
}

// SupportedVersions is the codex capability table; the floor is the highest Min.
// minCodexVersion stays the execution floor; minLunaRouteCodexVersion stays the
// separate LunaRoute credential-safety floor (version.go). The 0.170.0 entries
// are evidence-inferred, not certified per release (RR-03 DECISION 2026-11-22,
// phase-1 task-12 plan); shapes were observed on 0.171.0.
var SupportedVersions = []HarnessFloor{
	{Feature: "exec --json event stream", Min: minCodexVersion},
	{Feature: "rollout codex_error_info and token_count rate_limits", Min: "0.170.0"},
	{Feature: "app-server account/rateLimits/read ordinaryUsageAllowed", Min: "0.170.0"},
}

// ErrCodexMissing means the codex binary could not be started.
var ErrCodexMissing = errors.New("codex: binary not found")

// CheckVersion reports whether v satisfies every SupportedVersions floor.
func CheckVersion(v string) (bool, string) {
	found := parseVersion(v)
	if found == nil {
		if strings.TrimSpace(v) == "" {
			return false, "codex version unknown"
		}
		return false, fmt.Sprintf("codex version %q is not parseable", v)
	}
	floor := SupportedVersions[0]
	for _, f := range SupportedVersions[1:] {
		if compareVersion(mustParseVersion(f.Min), mustParseVersion(floor.Min)) > 0 {
			floor = f
		}
	}
	if compareVersion(found, mustParseVersion(floor.Min)) < 0 {
		return false, fmt.Sprintf("codex %s is below %s (%s)", v, floor.Min, floor.Feature)
	}
	return true, ""
}

// VersionEvidence separates a MISSING binary (err wraps ErrCodexMissing) from
// an INCOMPATIBLE one (compatible=false with a reason, err nil).
func VersionEvidence(ctx context.Context, cfg Config) (string, bool, string, error) {
	runner := cfg.Runner
	if runner == nil {
		runner = claudecli.NewExecRunner()
	}
	probeCtx, cancel := procenv.WithTimeout(ctx, probeTimeout)
	defer cancel()
	lines, wait, err := runner.Run(probeCtx, claudecli.RunSpec{
		Bin:              resolveBinary(cfg.BinaryPath),
		Args:             []string{"--version"},
		Env:              codexEnv(),
		MaxResponseBytes: probeMaxResponseBytes,
	})
	if err != nil {
		return "", false, "codex binary not found", fmt.Errorf("%w: %w", ErrCodexMissing, claudecli.SanitizeProcessError(err))
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if werr := wait(); werr != nil {
		return "", false, "codex --version failed", nil
	}
	found := parseVersion(strings.Join(out, "\n"))
	if found == nil {
		return "", false, "codex --version output is not parseable", nil
	}
	v := fmt.Sprintf("%d.%d.%d", found[0], found[1], found[2])
	if compareVersion(found, mustParseVersion(minCodexVersion)) < 0 {
		return v, false, fmt.Sprintf("codex %s is below the execution floor %s", v, minCodexVersion), nil
	}
	if ok, reason := CheckVersion(v); !ok {
		return v, false, reason, nil
	}
	return v, true, "", nil
}
