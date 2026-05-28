package claudecli

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
)

// argsPolicy carries operator-policy knobs that are not request-scoped.
// They originate from the provider Config (which comes from cfg.LLM.*) and
// are passed down to buildArgs so the function stays a pure data-in / args-out
// helper with no provider-struct coupling.
type argsPolicy struct {
	DefaultModel    string
	SkipPermissions bool
	AllowedTools    []string
}

// buildArgs assembles the `claude` CLI arguments for one invocation.
//
// The required protocol flags (`--print`, `--verbose`, `--output-format
// stream-json`) are always present — the adapter cannot function without them.
// Operator-policy flags (skip-permissions, allowed-tools) come from policy.
//
// SECURITY: `--dangerously-skip-permissions` is only emitted when
// policy.SkipPermissions is true. The default in cfg.LLM.SkipPermissions is
// false; flipping it allows the agent to run any tool (Bash, Write, ...) with
// no human confirmation, which is RCE-equivalent when prompt content is
// influenced by untrusted input. Operators flipping the default MUST also
// configure cfg.LLM.AllowedTools so the agent runs against an explicit
// allowlist.
func buildArgs(req engine.LLMRequest, policy argsPolicy) []string {
	args := []string{
		"--print", "--verbose", "--output-format", "stream-json",
	}
	if policy.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if len(policy.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(policy.AllowedTools, ","))
	}

	model := req.Model
	if model == "" {
		model = policy.DefaultModel
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if req.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", req.SystemPrompt)
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	return args
}

// standardBinDirs are the directories appended to PATH so the agent subprocess
// can find common tools (git, gh, claude itself). Order matters: ~/.local/bin
// is preferred so user-local installs win.
func standardBinDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		"/usr/local/bin", "/opt/homebrew/bin",
	}
	if home != "" {
		dirs = append([]string{filepath.Join(home, ".local", "bin")}, dirs...)
	}
	return dirs
}

// resolveBinary returns the claude binary path. An explicit path is returned
// unchanged; an empty path triggers a PATH lookup then standardBinDirs.
func resolveBinary(explicit string) string {
	return resolveBinaryWith(explicit, exec.LookPath, os.Stat)
}

// resolveBinaryWith is the testable form of resolveBinary; the dependencies on
// exec.LookPath and os.Stat are injected. Production callers use resolveBinary.
func resolveBinaryWith(
	explicit string,
	lookPath func(string) (string, error),
	stat func(string) (os.FileInfo, error),
) string {
	if explicit != "" {
		// SECURITY (PR #8 thread 3291945444, low): if an operator passes an
		// explicit BinaryPath that any other local user can overwrite, log a
		// warning. The path is still used (operators may have legitimate
		// reasons), but the misconfiguration is now visible.
		if fi, err := stat(explicit); err == nil {
			if fi.Mode()&0o022 != 0 {
				slog.Warn(
					"claudecli: explicit BinaryPath is group- or world-writable",
					"path", explicit,
					"mode", fi.Mode().String(),
				)
			}
		}
		return explicit
	}
	if p, err := lookPath("claude"); err == nil {
		return p
	}
	for _, dir := range standardBinDirs() {
		c := filepath.Join(dir, "claude")
		if _, err := stat(c); err == nil {
			return c
		}
	}
	return "claude"
}

// envAllowlist is the set of parent-process environment variables that may be
// propagated to the claude subprocess.
//
// SECURITY: any var NOT in this allowlist is stripped. The default os.Environ()
// leaks every secret the engine was started with (ANTHROPIC_API_KEY,
// GITHUB_TOKEN, AWS_*, DATABASE_URL, ...) to the agent process running under
// --dangerously-skip-permissions. See PR #8 security thread 3291942736.
//
// LC_* is handled by prefix match in augmentedEnv (LC_ALL, LC_CTYPE, LC_TIME,
// LC_MESSAGES, ...).
var envAllowlist = map[string]bool{
	"PATH":              true,
	"HOME":              true,
	"USER":              true,
	"LOGNAME":           true,
	"LANG":              true,
	"LC_ALL":            true,
	"LC_CTYPE":          true,
	"TERM":              true,
	"TMPDIR":            true,
	"SHELL":             true,
	"CLAUDE_CONFIG_DIR": true,
}

// augmentedEnv returns the allowlist-filtered parent env with standardBinDirs
// appended to PATH (or a synthesised PATH if the parent had none) so the agent
// subprocess can find common tools.
func augmentedEnv() []string {
	parent := os.Environ()
	filtered := make([]string, 0, len(envAllowlist))
	var parentPath string
	havePath := false

	for _, e := range parent {
		i := strings.IndexByte(e, '=')
		if i <= 0 {
			continue
		}
		key := e[:i]
		if !envAllowlist[key] && !strings.HasPrefix(key, "LC_") {
			continue
		}
		if key == "PATH" {
			parentPath = e[i+1:]
			havePath = true
			continue // re-emit below with augmentation
		}
		filtered = append(filtered, e)
	}

	// Build the augmented PATH: parent entries first, then standardBinDirs
	// appended uniquely. When the parent had no PATH at all, just emit the
	// standard dirs so the agent can still find git/gh/claude.
	existing := make(map[string]bool)
	path := parentPath
	if havePath {
		for _, p := range filepath.SplitList(parentPath) {
			existing[p] = true
		}
	}
	for _, d := range standardBinDirs() {
		if existing[d] {
			continue
		}
		if path == "" {
			path = d
		} else {
			path += string(os.PathListSeparator) + d
		}
		existing[d] = true
	}
	filtered = append(filtered, "PATH="+path)
	return filtered
}
