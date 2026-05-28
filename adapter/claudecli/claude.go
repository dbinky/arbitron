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

