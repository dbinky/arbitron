package opencode

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
)

// minOpencodeVersion is the minimum opencode release with the 1.17.x `run`
// contract this adapter targets. The 0.x line is REJECTED: its `run` CLI took
// `--allow-all-tools`/`--allowed-tools` (removed in 1.x), honored per-provider
// base-URL env vars (ignored on 1.x's routing path), and emitted a different
// output format. 1.x drives permissions/base-urls via config (OPENCODE_CONFIG_CONTENT)
// and `--dangerously-skip-permissions`. Verified against opencode-ai 1.17.7.
// Package: npm install -g opencode-ai (>= 1.17.0). Binary name: `opencode`.
const minOpencodeVersion = "1.17.0"

// minOpencodeProbeVersion is the oldest opencode whose D12 probe isolation was
// verified against the installed binary: `debug config` resolves every layer,
// the inline OPENCODE_CONFIG_CONTENT overlay's per-server enabled:false stops
// inherited MCP servers, and --pure stops inherited plugins. See
// testdata/probe-restrictions.md. Ordinary missions keep minOpencodeVersion.
const minOpencodeProbeVersion = "1.19.3"

// ErrIncompatibleOpencode is returned when the resolved opencode binary is older
// than minOpencodeVersion or reports an unparseable version.
var ErrIncompatibleOpencode = errors.New("opencode: incompatible binary version")

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// probeVersion runs `<bin> --version`, parses the first semver-looking token,
// and returns ErrIncompatibleOpencode if it is below minOpencodeVersion or
// unparseable. It uses the shared CommandRunner seam so it is hermetically
// testable with the fake runner.
func probeVersion(ctx context.Context, runner claudecli.CommandRunner, bin string) error {
	_, err := probeVersionNumber(ctx, runner, bin, procenv.BaseEnv(), "")
	return err
}

// probeVersionNumber is probeVersion returning the parsed version, so the
// discovery probe can apply its own (stricter) floor. SECURITY: env and dir are
// set explicitly on the RunSpec — a nil Env would inherit the whole server
// environment (ARBITRON_MASTER_KEY, unrelated provider keys) into the child.
// Callers pass the allowlisted procenv.BaseEnv(); the version check needs no
// credential.
func probeVersionNumber(ctx context.Context, runner claudecli.CommandRunner, bin string, env []string, dir string) ([]int, error) {
	if env == nil {
		env = procenv.BaseEnv()
	}
	lines, wait, err := runner.Run(ctx, claudecli.RunSpec{Bin: bin, Args: []string{"--version"}, Env: env, Dir: dir})
	if err != nil {
		return nil, fmt.Errorf("opencode: version probe: %w", err)
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if waitErr := wait(); waitErr != nil {
		return nil, fmt.Errorf("opencode: version probe: %w", waitErr)
	}
	found := parseVersion(strings.Join(out, "\n"))
	if found == nil {
		return nil, fmt.Errorf("%w: could not parse version from %q (require >= %s)", ErrIncompatibleOpencode, strings.Join(out, " "), minOpencodeVersion)
	}
	if compareVersion(found, mustParseVersion(minOpencodeVersion)) < 0 {
		return nil, fmt.Errorf("%w: found %d.%d.%d, require >= %s", ErrIncompatibleOpencode, found[0], found[1], found[2], minOpencodeVersion)
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
		panic("opencode: bad minOpencodeVersion constant: " + s)
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
