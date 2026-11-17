package claudecli

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
)

// argsPolicy carries operator-policy knobs that are not request-scoped.
// They originate from the provider Config (which comes from cfg.LLM.*) and
// are passed down to buildArgs so the function stays a pure data-in / args-out
// helper with no provider-struct coupling.
type argsPolicy struct {
	DefaultModel    string
	SkipPermissions bool // agent: emits --dangerously-skip-permissions
	SuppressTools   bool // chat: emits --allowedTools "" (empty set)
	AllowedTools    []string
	isolateSettings bool // LunaRoute only: no user/project/local settings overrides
}

// buildArgs assembles the `claude` CLI arguments for one invocation.
//
// The required protocol flags (`--print`, `--verbose`, `--output-format
// stream-json`) are always present — the adapter cannot function without them.
// Operator-policy flags (skip-permissions, allowed-tools) come from policy.
//
// SECURITY: `--dangerously-skip-permissions` is only emitted when
// policy.SkipPermissions is true. It runs the agent with no human confirmation,
// which is RCE-equivalent when prompt content is influenced by untrusted input
// (e.g. a pr-review persona reading an attacker-authored PR diff).
//
// SECURITY — VERIFIED (PR #18): the --allowedTools allowlist is NOT a security
// boundary for a skip-permissions agent. Empirical testing of the claude CLI
// (2.1.x) showed --dangerously-skip-permissions BYPASSES --allowedTools
// entirely: with skip-permissions set, a tool outside the allowlist — and even
// an empty allowlist — still executes (permission_denials comes back empty).
// The allowlist is enforced ONLY when skip-permissions is absent (then a
// disallowed tool is cleanly denied in headless mode, not prompted). Because
// agent profiles always set skip-permissions, the AllowedTools emitted below
// are DEFENSE-IN-DEPTH / intent only: they bound a hypothetical
// non-skip-permissions invocation and document what a profile is meant to
// touch, but they do NOT contain a prompt-injected agent. The real isolation
// boundary is the OS/container the agent runs in — it MUST be a sandbox with no
// host secrets and controlled network egress. See the PR #18 verification.
//
// policy.SuppressTools produces a no-tools chat invocation: it emits
// `--allowedTools ""` (an empty allowed-tools set) and ignores AllowedTools,
// so `claude -p` behaves as a plain completion with no tool access. It is
// mutually exclusive with SkipPermissions in practice and wins on the
// allowed-tools argument.
func buildArgs(req engine.ChatRequest, policy argsPolicy) []string {
	args := []string{
		"--print", "--verbose", "--output-format", "stream-json",
	}
	if policy.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	switch {
	case policy.SuppressTools:
		// Empty allowed-tools set: no tools available to the model.
		args = append(args, "--allowedTools", "")
	case len(policy.AllowedTools) > 0:
		args = append(args, "--allowedTools", strings.Join(policy.AllowedTools, ","))
	case policy.SkipPermissions:
		// Emit the empty allowlist for defense-in-depth / intent only. NOTE
		// (VERIFIED, PR #18): under --dangerously-skip-permissions the claude
		// CLI ignores --allowedTools, so this does NOT restrict a
		// skip-permissions agent — it only takes effect if skip-permissions is
		// ever disabled. The container is the real boundary (see the SECURITY
		// note on buildArgs above).
		args = append(args, "--allowedTools", "")
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
	if policy.isolateSettings {
		args = append(args, "--setting-sources", "")
	}
	return args
}

// resolveBinary returns the claude binary path. An explicit path is returned
// unchanged; an empty path triggers a PATH lookup then procenv.StandardBinDirs.
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
	for _, dir := range procenv.StandardBinDirs() {
		c := filepath.Join(dir, "claude")
		if _, err := stat(c); err == nil {
			return c
		}
	}
	return "claude"
}
