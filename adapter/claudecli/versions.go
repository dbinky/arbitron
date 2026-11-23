package claudecli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
)

// HarnessFloor names a harness feature arbitron depends on and the minimum
// version that provides it (evidence: docs/evidence/remote-runners/research-exhaustion.md).
type HarnessFloor struct {
	Feature string
	Min     string
}

// SupportedVersions is the claude capability table. The effective floor is the
// highest Min. Floors are inferred from session-log and binary-schema evidence
// and are not certified by real captures (RR-03 decision 2026-11-22).
var SupportedVersions = []HarnessFloor{
	{Feature: "rate_limit_event", Min: "2.1.209"},
	{Feature: "result usage cache buckets", Min: "2.1.278"},
	{Feature: "assistant error kind and quotaLimits", Min: "2.1.284"},
}

// ErrVersionUnparseable reports `claude --version` output without a semver.
var ErrVersionUnparseable = errors.New("claudecli: unparseable claude --version output")

var semverRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

// CheckVersion reports whether v meets every SupportedVersions floor. reason is
// "" when ok, else names the first unmet feature.
func CheckVersion(v string) (bool, string) {
	if strings.TrimSpace(v) == "" {
		return false, "claude version unknown"
	}
	have, ok := parseSemver(v)
	if !ok {
		return false, fmt.Sprintf("claude version %q is not parseable", v)
	}
	floor := HarnessFloor{}
	var floorV [3]int
	for _, f := range SupportedVersions {
		fv, _ := parseSemver(f.Min)
		if cmpSemver(fv, floorV) > 0 {
			floor, floorV = f, fv
		}
	}
	if cmpSemver(have, floorV) < 0 {
		return false, fmt.Sprintf("claude %s is below %s (%s)", v, floor.Min, floor.Feature)
	}
	return true, ""
}

func parseSemver(s string) ([3]int, bool) {
	m := semverRE.FindString(s)
	if m == "" {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range strings.SplitN(m, ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func cmpSemver(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// probeVersionTimeout bounds `claude --version`.
const probeVersionTimeout = 10 * time.Second

// ProbeVersion runs `<bin> --version` and returns the first semver it prints.
// Process failures are sanitized (no stderr reaches the error).
func ProbeVersion(ctx context.Context, r CommandRunner, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeVersionTimeout)
	defer cancel()
	lines, wait, err := r.Run(ctx, RunSpec{Bin: bin, Args: []string{"--version"}, Env: procenv.BaseEnv(), MaxResponseBytes: 64 << 10})
	if err != nil {
		return "", fmt.Errorf("claudecli: version probe: %w", SanitizeProcessError(err))
	}
	var first string
	for l := range lines {
		if first == "" && strings.TrimSpace(l) != "" {
			first = l
		}
	}
	if werr := wait(); werr != nil {
		return "", fmt.Errorf("claudecli: version probe: %w", SanitizeProcessError(werr))
	}
	v := semverRE.FindString(first)
	if v == "" {
		return "", ErrVersionUnparseable
	}
	return v, nil
}

// ResolveBinary returns the claude binary path and whether it was actually
// found. An explicit path counts as found; the bare "claude" fallback does not.
func ResolveBinary(explicit string) (string, bool) {
	return resolveBinaryFound(explicit, exec.LookPath, func(p string) (bool, error) {
		_, err := os.Stat(p)
		return err == nil, nil
	})
}

func resolveBinaryFound(explicit string, lookPath func(string) (string, error), exists func(string) (bool, error)) (string, bool) {
	if explicit != "" {
		return resolveBinary(explicit), true
	}
	if p, err := lookPath("claude"); err == nil {
		return p, true
	}
	for _, dir := range procenv.StandardBinDirs() {
		c := filepath.Join(dir, "claude")
		if ok, _ := exists(c); ok {
			return c, true
		}
	}
	return "claude", false
}
