// domain/runner/hub_compat.go
package runner

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/config"
)

// CompatTable is the server's admission table (design phase 2 §3.2).
// Harness maps a harness name ("claude", "codex") to its minimum version; a
// harness the runner does not report is not checked (the runner simply has no
// such source). RunnerMin "unreleased" disables remote execution while preserving built-in operation.
type CompatTable struct {
	ProtocolMin, ProtocolMax int
	RunnerMin                string
	RunnerMaxExclusive       string
	Harness                  map[string]string
}

// Check classifies a hello. Incompatible runners stay visible but receive no
// work; chat_only runners receive only hermetic (AffinityNone) work because
// their execution root differs from the server's.
func (c CompatTable) Check(h HelloInfo, serverRoot string) Compatibility {
	if h.OS != "darwin" {
		return Compatibility{State: CompatIncompatible, Reason: fmt.Sprintf("unsupported OS %q: remote runners require macOS (darwin)", h.OS)}
	}
	if h.ProtocolVersion < c.ProtocolMin || h.ProtocolVersion > c.ProtocolMax {
		return Compatibility{State: CompatIncompatible, Reason: fmt.Sprintf("protocol version %d outside supported range %d-%d", h.ProtocolVersion, c.ProtocolMin, c.ProtocolMax)}
	}
	if c.RunnerMin == "" || c.RunnerMaxExclusive == "" || !versionAtLeast(h.RunnerVersion, c.RunnerMin) || versionAtLeast(h.RunnerVersion, c.RunnerMaxExclusive) {
		return Compatibility{State: CompatIncompatible, Reason: fmt.Sprintf("runner version %q is older than the minimum %q", h.RunnerVersion, c.RunnerMin)}
	}
	names := make([]string, 0, len(c.Harness))
	for name := range c.Harness {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		have, ok := h.HarnessVersions[name]
		if !ok || strings.TrimSpace(have) == "" {
			continue
		}
		if !versionAtLeast(have, c.Harness[name]) {
			return Compatibility{State: CompatIncompatible, Reason: fmt.Sprintf("harness %s %q is older than the minimum %q", name, have, c.Harness[name])}
		}
	}
	if h.ScratchRoot == "" || !filepath.IsAbs(h.ScratchRoot) {
		return Compatibility{State: CompatIncompatible, Reason: fmt.Sprintf("scratch root %q must be an absolute path", h.ScratchRoot)}
	}
	if h.WorkspaceRoot != "" && filepath.IsAbs(h.WorkspaceRoot) && within(filepath.Clean(h.ScratchRoot), filepath.Clean(h.WorkspaceRoot)) {
		return Compatibility{State: CompatIncompatible, Reason: fmt.Sprintf("scratch root %q must be outside the workspace root %q", h.ScratchRoot, h.WorkspaceRoot)}
	}
	if serverRoot == "" || h.WorkspaceRoot == "" || !filepath.IsAbs(h.WorkspaceRoot) ||
		filepath.Clean(h.WorkspaceRoot) != filepath.Clean(serverRoot) {
		return Compatibility{State: CompatChatOnly, Reason: fmt.Sprintf("workspace root %q does not match the server root %q: hermetic chat only", h.WorkspaceRoot, serverRoot)}
	}
	return Compatibility{State: CompatCompatible}
}

// within reports whether child is parent or below it.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// parseVersion reads the leading dotted-numeric version of strings such as
// "2.1.30 (Claude Code)", "v0.3.0" or "0.50.0-beta".
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, " \t("); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// versionAtLeast is false when either side is unparseable (fail closed).
func versionAtLeast(have, min string) bool {
	h, ok1 := parseVersion(have)
	m, ok2 := parseVersion(min)
	if !ok1 || !ok2 {
		return false
	}
	at := func(v []int, i int) int {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	for i := 0; i < len(h) || i < len(m); i++ {
		if a, b := at(h, i), at(m, i); a != b {
			return a > b
		}
	}
	return true
}

// HubConfigFromRunners converts the validated runners config (P2 T2) into the
// hub's durations. serverRoot is made absolute so the root comparison in Check
// is meaningful for the default relative "./workspaces".
func HubConfigFromRunners(rc config.RunnersConfig, serverRoot string, compat CompatTable) HubConfig {
	root := serverRoot
	if serverRoot != "" {
		if abs, err := filepath.Abs(serverRoot); err == nil {
			root = abs
		}
	}
	return HubConfig{
		Heartbeat:           time.Duration(rc.HeartbeatSeconds) * time.Second,
		SuspectAfter:        time.Duration(rc.SuspectAfterSeconds) * time.Second,
		LostAfter:           time.Duration(rc.LostAfterSeconds) * time.Second,
		OfferTTL:            time.Duration(rc.OfferTTLSeconds) * time.Second,
		TelemetryWindow:     rc.TelemetryWindow,
		ServerWorkspaceRoot: root,
		Compat:              compat,
	}
}
