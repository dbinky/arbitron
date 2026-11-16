package pi

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

// minPiVersion is the minimum pi release whose non-interactive flag surface
// (--print, --mode json, --no-session, --model, --thinking) and JSON event
// vocabulary match what args.go and parse.go pin. VERIFIED against pi 0.87.0 at
// implementation time. Package: @earendil-works/pi-coding-agent; binary: `pi`.
const minPiVersion = "0.87.0"

// minPiToolFreeVersion is the earliest pi release whose tool, MCP, extension
// and update restrictions this adapter has verified (testdata/probe-restrictions.md).
// The tool-disabled exact-model probe refuses an older binary rather than risk
// a turn with tools enabled; regular missions keep minPiVersion.
const minPiToolFreeVersion = "1.3.0"

// minLunaRoutePiVersion is the minimum pi release for a LunaRoute launch. It is
// higher than minPiVersion because a LunaRoute launch hands the binary a
// credential and depends on controls the CLI-compatibility floor never
// established: PI_CODING_AGENT_DIR relocation of the whole agent directory,
// project .pi config/extensions not loading from a private (untrusted) agent
// directory, and $LUNAROUTE_API_KEY interpolation in models.json. It is the
// oldest release a committed record verifies them on:
// docs/evidence/lunaroute-provider/observations/lr08-pi-model-controls.json
// (pi 1.5.2). Subscription launches keep minPiVersion.
const minLunaRoutePiVersion = "1.5.2"

// requireLunaRouteVersion refuses a binary older than minLunaRoutePiVersion.
// It is applied only on the LunaRoute launch path, never to subscription use.
func requireLunaRouteVersion(found []int) error {
	if len(found) != 3 {
		return fmt.Errorf("%w: unknown version (LunaRoute launches require >= %s)", ErrIncompatiblePi, minLunaRoutePiVersion)
	}
	if compareVersion(found, mustParseVersion(minLunaRoutePiVersion)) < 0 {
		return fmt.Errorf("%w: found %d.%d.%d, LunaRoute launches require >= %s",
			ErrIncompatiblePi, found[0], found[1], found[2], minLunaRoutePiVersion)
	}
	return nil
}

// probeMaxResponseBytes caps cumulative stdout for the `--version` probe. A
// version string is tiny, so a small ceiling is ample; it bounds the memory a
// wedged or hostile `pi` build can grow by streaming unbounded output on a
// probe. 64 KiB leaves generous slack over any real probe output.
const probeMaxResponseBytes int64 = 64 * 1024

// probeTimeout bounds the probe subprocess so a pi build that never EOFs cannot
// pin a probe goroutine indefinitely.
const probeTimeout = 10 * time.Second

// ErrIncompatiblePi is returned when the resolved pi binary is older than
// minPiVersion or reports an unparseable version.
var ErrIncompatiblePi = errors.New("pi: incompatible binary version")

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// probeVersion runs `<bin> --version`, parses the first semver-looking token,
// and returns it, or ErrIncompatiblePi if it is below minPiVersion or
// unparseable. It uses the shared claudecli.CommandRunner seam so it is
// hermetically testable with the fake runner.
//
// SECURITY: env is the explicit child environment for the probe. Callers MUST
// pass a built env, never nil — a nil Env makes os/exec INHERIT the engine's
// full parent environment (master key, provider keys, DSNs) into a
// user-installed `pi` binary resolved by name from PATH.
func probeVersion(ctx context.Context, runner claudecli.CommandRunner, bin string, env []string) ([]int, error) {
	probeCtx, cancel := procenv.WithTimeout(ctx, probeTimeout)
	defer cancel()

	lines, wait, err := runner.Run(probeCtx, claudecli.RunSpec{
		Bin:              bin,
		Args:             []string{"--version"},
		Env:              env,
		MaxResponseBytes: probeMaxResponseBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("pi: version probe: %w", err)
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if waitErr := wait(); waitErr != nil {
		return nil, fmt.Errorf("pi: version probe: %w", waitErr)
	}
	found := parseVersion(strings.Join(out, "\n"))
	if found == nil {
		return nil, fmt.Errorf("%w: could not parse version from %q (require >= %s)",
			ErrIncompatiblePi, strings.Join(out, " "), minPiVersion)
	}
	if compareVersion(found, mustParseVersion(minPiVersion)) < 0 {
		return nil, fmt.Errorf("%w: found %d.%d.%d, require >= %s",
			ErrIncompatiblePi, found[0], found[1], found[2], minPiVersion)
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
		panic("pi: bad minPiVersion constant: " + s)
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
