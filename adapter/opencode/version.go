package opencode

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// minOpencodeVersion is the minimum modern (SST) opencode release with a stable
// `run -m provider/model` contract and config-driven permissions. Earlier builds
// (e.g. the legacy v0.0.55 Go variant) lack `run`/`-m` and are rejected.
// Package: npm install -g opencode-ai (>= 0.4.0). Binary name: `opencode`.
const minOpencodeVersion = "0.4.0"

// ErrIncompatibleOpencode is returned when the resolved opencode binary is older
// than minOpencodeVersion or reports an unparseable version.
var ErrIncompatibleOpencode = errors.New("opencode: incompatible binary version")

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// probeVersion runs `<bin> --version`, parses the first semver-looking token,
// and returns ErrIncompatibleOpencode if it is below minOpencodeVersion or
// unparseable. It uses the shared CommandRunner seam so it is hermetically
// testable with the fake runner.
func probeVersion(ctx context.Context, runner claudecli.CommandRunner, bin string) error {
	lines, wait, err := runner.Run(ctx, claudecli.RunSpec{Bin: bin, Args: []string{"--version"}})
	if err != nil {
		return fmt.Errorf("opencode: version probe: %w", err)
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if waitErr := wait(); waitErr != nil {
		return fmt.Errorf("opencode: version probe: %w", waitErr)
	}
	found := parseVersion(strings.Join(out, "\n"))
	if found == nil {
		return fmt.Errorf("%w: could not parse version from %q (require >= %s)", ErrIncompatibleOpencode, strings.Join(out, " "), minOpencodeVersion)
	}
	if compareVersion(found, mustParseVersion(minOpencodeVersion)) < 0 {
		return fmt.Errorf("%w: found %d.%d.%d, require >= %s", ErrIncompatibleOpencode, found[0], found[1], found[2], minOpencodeVersion)
	}
	return nil
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
