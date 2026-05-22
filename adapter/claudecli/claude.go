package claudecli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
)

// buildArgs assembles the `claude` CLI arguments for one invocation.
// defaultModel is used when the request does not name a model.
func buildArgs(req engine.LLMRequest, defaultModel string) []string {
	args := []string{
		"--print", "--verbose", "--output-format", "stream-json",
		"--dangerously-skip-permissions",
	}

	model := req.Model
	if model == "" {
		model = defaultModel
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
