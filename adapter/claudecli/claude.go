package claudecli

import (
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

// resolveBinary returns the claude binary path. An explicit path is returned
// unchanged; an empty path triggers a PATH lookup then common install dirs.
func resolveBinary(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, ".local", "bin", "claude"),
		"/usr/local/bin/claude",
		"/opt/homebrew/bin/claude",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "claude"
}

// augmentedEnv returns the process environment with common binary directories
// appended to PATH so the agent subprocess can find git/gh.
func augmentedEnv() []string {
	env := os.Environ()
	home, _ := os.UserHomeDir()
	extra := []string{
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin", "/opt/homebrew/bin",
	}
	for i, e := range env {
		if !strings.HasPrefix(e, "PATH=") {
			continue
		}
		path := strings.TrimPrefix(e, "PATH=")
		existing := make(map[string]bool)
		for _, p := range filepath.SplitList(path) {
			existing[p] = true
		}
		for _, d := range extra {
			if !existing[d] {
				path += string(os.PathListSeparator) + d
				existing[d] = true
			}
		}
		env[i] = "PATH=" + path
		break
	}
	return env
}
