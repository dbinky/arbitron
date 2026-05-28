package claudecli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestBuildArgs(t *testing.T) {
	t.Run("base args are always present (skip-permissions off, no allowed tools)", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.Equal(t, []string{
			"--print", "--verbose", "--output-format", "stream-json",
		}, args)
	})

	t.Run("skip-permissions emits --dangerously-skip-permissions when true", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{SkipPermissions: true})
		assert.Contains(t, args, "--dangerously-skip-permissions")
	})

	t.Run("skip-permissions omits the flag when false", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{SkipPermissions: false})
		assert.NotContains(t, args, "--dangerously-skip-permissions")
	})

	t.Run("allowed tools are forwarded as a comma-joined --allowedTools", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{AllowedTools: []string{"Read", "Bash(git status:*)"}})
		assert.Contains(t, args, "--allowedTools")
		for i, a := range args {
			if a == "--allowedTools" {
				assert.Equal(t, "Read,Bash(git status:*)", args[i+1])
				return
			}
		}
		t.Fatal("--allowedTools not found")
	})

	t.Run("empty allowed tools omits the flag", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.NotContains(t, args, "--allowedTools")
	})

	t.Run("request model overrides default model", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{Model: "claude-opus-4-7"}, argsPolicy{DefaultModel: "claude-sonnet-4-6"})
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-opus-4-7")
		assert.NotContains(t, args, "claude-sonnet-4-6")
	})

	t.Run("default model used when request model is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{DefaultModel: "claude-sonnet-4-6"})
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-sonnet-4-6")
	})

	t.Run("no --model flag when both are empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.NotContains(t, args, "--model")
	})

	t.Run("session id adds --resume", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SessionID: "sess-9"}, argsPolicy{})
		assert.Contains(t, args, "--resume")
		assert.Contains(t, args, "sess-9")
	})

	t.Run("no --resume when session id is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.NotContains(t, args, "--resume")
	})

	t.Run("system prompt adds --append-system-prompt", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SystemPrompt: "be terse"}, argsPolicy{})
		assert.Contains(t, args, "--append-system-prompt")
		assert.Contains(t, args, "be terse")
	})
}

func TestAugmentedEnv(t *testing.T) {
	t.Run("appends a standard dir even when a substring-prefix dir is present", func(t *testing.T) {
		// PATH contains /usr/local/bin-custom, which substring-contains
		// "/usr/local/bin" but is NOT the same entry.
		t.Setenv("PATH", "/usr/local/bin-custom")
		env := augmentedEnv()

		var pathVal string
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				pathVal = strings.TrimPrefix(e, "PATH=")
			}
		}
		entries := filepath.SplitList(pathVal)
		assert.Contains(t, entries, "/usr/local/bin",
			"/usr/local/bin must be added as a distinct entry")
		assert.Contains(t, entries, "/usr/local/bin-custom")
	})

	t.Run("does not duplicate a dir already present as an exact entry", func(t *testing.T) {
		t.Setenv("PATH", "/usr/local/bin")
		env := augmentedEnv()

		var pathVal string
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				pathVal = strings.TrimPrefix(e, "PATH=")
			}
		}
		count := 0
		for _, p := range filepath.SplitList(pathVal) {
			if p == "/usr/local/bin" {
				count++
			}
		}
		assert.Equal(t, 1, count, "/usr/local/bin must appear exactly once")
	})
}

func TestResolveBinary(t *testing.T) {
	t.Run("explicit path is returned unchanged", func(t *testing.T) {
		assert.Equal(t, "/custom/claude", resolveBinary("/custom/claude"))
	})

	t.Run("empty path resolves to a non-empty candidate", func(t *testing.T) {
		assert.NotEmpty(t, resolveBinary(""))
	})
}
