// Package procenv builds the controlled subprocess environment shared by the
// claude-cli and process adapters. It exposes a base allowlist (no wholesale
// inheritance of the engine's secrets) and PATH augmentation.
package procenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WithTimeout derives a context honouring d. A non-positive d yields the parent
// ctx unchanged with a no-op cancel (no deadline). It is the single shared
// helper for the per-call timeout plumbing used by the claudecli, opencode,
// openrouter, and azurefoundry adapters. The returned CancelFunc must always be
// called (typically deferred) to release resources.
func WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// baseAllowlist is the set of parent-process environment variables propagated
// to subprocesses. SECURITY: any var NOT here (and not LC_*) is stripped, so
// secrets the engine started with (ANTHROPIC_API_KEY, DB URLs, ...) never leak.
var baseAllowlist = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"LANG": true, "TERM": true, "TMPDIR": true, "SHELL": true,
	"CLAUDE_CONFIG_DIR": true,
}

// IsBaseName reports whether name is part of the base allowlist (including the
// LC_* locale prefix). Used to reject per-node env keys that would shadow it.
func IsBaseName(name string) bool {
	return baseAllowlist[name] || strings.HasPrefix(name, "LC_")
}

// StandardBinDirs are directories appended to PATH so subprocesses can find
// common tools (git, gh, claude). ~/.local/bin is preferred.
func StandardBinDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin"}
	if home != "" {
		dirs = append([]string{filepath.Join(home, ".local", "bin")}, dirs...)
	}
	return dirs
}

// BaseEnv returns the allowlist-filtered copy of the current process
// environment, with PATH augmented by StandardBinDirs. This is the safe base
// for subprocesses.
func BaseEnv() []string {
	parent := os.Environ()
	filtered := make([]string, 0, len(parent))
	var parentPath string
	havePath := false

	for _, e := range parent {
		i := strings.IndexByte(e, '=')
		if i <= 0 {
			continue
		}
		key := e[:i]
		if !baseAllowlist[key] && !strings.HasPrefix(key, "LC_") {
			continue
		}
		if key == "PATH" {
			parentPath = e[i+1:]
			havePath = true
			continue
		}
		filtered = append(filtered, e)
	}

	existing := make(map[string]bool)
	path := parentPath
	if havePath {
		for _, p := range filepath.SplitList(parentPath) {
			existing[p] = true
		}
	}
	for _, d := range StandardBinDirs() {
		if existing[d] {
			continue
		}
		if path == "" {
			path = d
		} else {
			path += string(os.PathListSeparator) + d
		}
		existing[d] = true // guard against duplicate StandardBinDirs entries
	}
	filtered = append(filtered, "PATH="+path)
	return filtered
}
