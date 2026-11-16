package codex

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
)

// minCodexVersion is the minimum codex-cli release whose `codex exec --json`
// event stream and flag surface (--dangerously-bypass-approvals-and-sandbox,
// --cd, --json) match what args.go/parse.go pin. VERIFIED against codex-cli
// 0.137.0 at implementation time. Package/binary name: `codex`.
const minCodexVersion = "0.137.0"

// minLunaRouteCodexVersion is the minimum codex-cli release for a LunaRoute
// launch. It is higher than minCodexVersion because a LunaRoute launch hands
// the binary a credential and depends on controls the CLI-compatibility floor
// never established: the private CODEX_HOME relocation, auth independence from
// the operator's login, and the model_providers.* / review_model overrides. It
// is the oldest release a committed record verifies those controls on:
// docs/evidence/lunaroute-provider/observations/lr07-codex-config-controls.json
// (codex-cli 0.167.2). Subscription launches keep minCodexVersion.
const minLunaRouteCodexVersion = "0.167.2"

// requireLunaRouteVersion refuses a binary older than minLunaRouteCodexVersion.
// It is applied only on the LunaRoute launch path, never to subscription use.
func requireLunaRouteVersion(found []int) error {
	if len(found) != 3 {
		return fmt.Errorf("%w: unknown version (LunaRoute launches require >= %s)", ErrIncompatibleCodex, minLunaRouteCodexVersion)
	}
	if compareVersion(found, mustParseVersion(minLunaRouteCodexVersion)) < 0 {
		return fmt.Errorf("%w: found %d.%d.%d, LunaRoute launches require >= %s", ErrIncompatibleCodex, found[0], found[1], found[2], minLunaRouteCodexVersion)
	}
	return nil
}

// probeMaxResponseBytes caps cumulative stdout for the `--version` and `login
// status` probes. A version string and a login-status line are tiny, so a small
// ceiling is ample; it bounds the memory a wedged/hostile codex build can grow
// by streaming unbounded output on a probe (security LOW — unlike the exec/stream
// paths, the probes otherwise read with no cap). 64 KiB leaves generous slack
// over any real probe output while still failing closed on a flood.
const probeMaxResponseBytes int64 = 64 * 1024

// probeTimeout bounds each probe subprocess so a codex build that never EOFs
// cannot pin a probe goroutine indefinitely. The probes are local, near-instant
// CLI calls, so a short ceiling is safe; it is the timeout analogue of the byte
// cap above and matches the defense the exec/stream paths get from their
// per-call timeout.
const probeTimeout = 10 * time.Second

// ErrIncompatibleCodex is returned when the resolved codex binary is older than
// minCodexVersion or reports an unparseable version.
var ErrIncompatibleCodex = errors.New("codex: incompatible binary version")

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// probeVersion runs `<bin> --version`, parses the first semver-looking token,
// and returns ErrIncompatibleCodex if it is below minCodexVersion or
// unparseable. It uses the shared claudecli.CommandRunner seam so it is
// hermetically testable with the fake runner (the SAME seam opencode and
// claudecli use — never redefined).
//
// SECURITY: env is the explicit child environment for the bare `codex --version`
// call. Callers MUST pass the subscription base env (codexEnv()), NOT nil — a nil
// Env makes os/exec INHERIT the engine's full parent environment (master key,
// ANTHROPIC_API_KEY, DSNs) into a user-installed `codex` binary resolved by name
// from PATH. Threading it here keeps the no-key env uniform and visible at every
// call site (batch / stream / readiness).
//
// The probe is bounded by probeMaxResponseBytes (a small stdout cap) and a
// derived probeTimeout, so a wedged/hostile codex build that never EOFs cannot
// grow memory or pin the probe goroutine indefinitely.
func probeVersion(ctx context.Context, runner claudecli.CommandRunner, bin string, env []string) error {
	_, err := compatibleVersion(ctx, runner, bin, env)
	return err
}

func compatibleVersion(ctx context.Context, runner claudecli.CommandRunner, bin string, env []string) ([]int, error) {
	probeCtx, cancel := procenv.WithTimeout(ctx, probeTimeout)
	defer cancel()

	lines, wait, err := runner.Run(probeCtx, claudecli.RunSpec{
		Bin:              bin,
		Args:             []string{"--version"},
		Env:              env,
		MaxResponseBytes: probeMaxResponseBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("codex: version probe: %w", claudecli.SanitizeProcessError(err))
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if waitErr := wait(); waitErr != nil {
		return nil, fmt.Errorf("codex: version probe: %w", claudecli.SanitizeProcessError(waitErr))
	}
	found := parseVersion(strings.Join(out, "\n"))
	if found == nil {
		return nil, fmt.Errorf("%w: could not parse version (require >= %s)", ErrIncompatibleCodex, minCodexVersion)
	}
	if compareVersion(found, mustParseVersion(minCodexVersion)) < 0 {
		return nil, fmt.Errorf("%w: found %d.%d.%d, require >= %s", ErrIncompatibleCodex, found[0], found[1], found[2], minCodexVersion)
	}
	return found, nil
}

func parseVersion(s string) []int {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	out := make([]int, 3)
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

func mustParseVersion(s string) []int {
	v := parseVersion(s)
	if v == nil {
		panic("codex: bad minCodexVersion constant: " + s)
	}
	return v
}

// compareVersion returns -1, 0, or 1 comparing a to b major.minor.patch.
func compareVersion(a, b []int) int {
	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}
