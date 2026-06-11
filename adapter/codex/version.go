package codex

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// minCodexVersion is the minimum codex-cli release whose `codex exec --json`
// event stream and flag surface (--dangerously-bypass-approvals-and-sandbox,
// --cd, --json) match what args.go/parse.go pin. VERIFIED against codex-cli
// 0.137.0 at implementation time. Package/binary name: `codex`.
const minCodexVersion = "0.137.0"

// ErrIncompatibleCodex is returned when the resolved codex binary is older than
// minCodexVersion or reports an unparseable version.
var ErrIncompatibleCodex = errors.New("codex: incompatible binary version")

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// probeVersion runs `<bin> --version`, parses the first semver-looking token,
// and returns ErrIncompatibleCodex if it is below minCodexVersion or
// unparseable. It uses the shared claudecli.CommandRunner seam so it is
// hermetically testable with the fake runner (the SAME seam opencode and
// claudecli use — never redefined).
func probeVersion(ctx context.Context, runner claudecli.CommandRunner, bin string) error {
	lines, wait, err := runner.Run(ctx, claudecli.RunSpec{Bin: bin, Args: []string{"--version"}})
	if err != nil {
		return fmt.Errorf("codex: version probe: %w", err)
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if waitErr := wait(); waitErr != nil {
		return fmt.Errorf("codex: version probe: %w", waitErr)
	}
	found := parseVersion(strings.Join(out, "\n"))
	if found == nil {
		return fmt.Errorf("%w: could not parse version from %q (require >= %s)", ErrIncompatibleCodex, strings.Join(out, " "), minCodexVersion)
	}
	if compareVersion(found, mustParseVersion(minCodexVersion)) < 0 {
		return fmt.Errorf("%w: found %d.%d.%d, require >= %s", ErrIncompatibleCodex, found[0], found[1], found[2], minCodexVersion)
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
