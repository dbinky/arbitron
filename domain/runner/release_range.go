package runner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// ReleaseRunnerRange admits compatible patch releases of the server's minor.
// Unstamped builds refuse remote execution while retaining local operation.
// Prerelease floors are additionally checked for exact identity at admission.
func ReleaseRunnerRange(serverVersion string) (min, maxExclusive string) {
	v := strings.TrimPrefix(serverVersion, "v")
	parts := releaseVersion.FindStringSubmatch(v)
	if parts == nil {
		return "unreleased", "unreleased"
	}
	minor, err := strconv.Atoi(parts[2])
	if err != nil || minor == int(^uint(0)>>1) {
		return "unreleased", "unreleased"
	}
	return v, fmt.Sprintf("%s.%d.0", parts[1], minor+1)
}

func runnerVersionAllowed(have, min, max string) bool {
	h := strings.TrimPrefix(have, "v")
	m := strings.TrimPrefix(min, "v")
	hp, mp := releaseVersion.FindStringSubmatch(h), releaseVersion.FindStringSubmatch(m)
	if hp == nil || mp == nil || max == "" {
		return false
	}
	if mp[4] != "" {
		return h == m
	}
	return hp[4] == "" && versionAtLeast(h, m) && !versionAtLeast(h, max)
}
