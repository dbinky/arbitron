package claudecli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestBuildArgs(t *testing.T) {
	t.Run("base args are always present", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "")
		assert.Equal(t, []string{
			"--print", "--verbose", "--output-format", "stream-json",
			"--dangerously-skip-permissions",
		}, args)
	})

	t.Run("request model overrides default model", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{Model: "claude-opus-4-7"}, "claude-sonnet-4-6")
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-opus-4-7")
		assert.NotContains(t, args, "claude-sonnet-4-6")
	})

	t.Run("default model used when request model is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "claude-sonnet-4-6")
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-sonnet-4-6")
	})

	t.Run("no --model flag when both are empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "")
		assert.NotContains(t, args, "--model")
	})

	t.Run("session id adds --resume", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SessionID: "sess-9"}, "")
		assert.Contains(t, args, "--resume")
		assert.Contains(t, args, "sess-9")
	})

	t.Run("no --resume when session id is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, "")
		assert.NotContains(t, args, "--resume")
	})

	t.Run("system prompt adds --append-system-prompt", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SystemPrompt: "be terse"}, "")
		assert.Contains(t, args, "--append-system-prompt")
		assert.Contains(t, args, "be terse")
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
