package engine_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowedMoveReason_OnlyRecoveryTriggers(t *testing.T) {
	for _, r := range []string{"exhausted", "logged_out", "drain", "runner_lost"} {
		assert.True(t, engine.AllowedMoveReason(r), r)
	}
	for _, r := range []string{"", "load", "cordon", "suspect", "slot", "transient"} {
		assert.False(t, engine.AllowedMoveReason(r), r)
	}
}

func TestBuildRebuildPrompt_KeepsFirstPromptNonceAndWholeTranscript(t *testing.T) {
	conv := map[string]any{
		"nonce": "n-123",
		"messages": []any{
			map[string]any{"role": "user", "content": "ORIGINAL PROMPT with sentinel nonce n-123"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "second question"},
			map[string]any{"role": "assistant", "content": "second answer"},
		},
	}
	got, err := engine.BuildRebuildPrompt(conv, "third question", 0)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, "ORIGINAL PROMPT with sentinel nonce n-123"), "the original first prompt (with its nonce) leads verbatim")
	for _, part := range []string{"first answer", "second question", "second answer"} {
		assert.Contains(t, got, part, "no recorded turn may be dropped or summarized")
	}
	assert.True(t, strings.HasSuffix(got, "third question"), "the next user message is appended last")
	assert.Equal(t, "ORIGINAL PROMPT with sentinel nonce n-123\n\n"+
		"The conversation so far, as recorded by Arbitron (earlier tool details and reasoning are not included):\n"+
		"\n[assistant]\nfirst answer\n\n[user]\nsecond question\n\n[assistant]\nsecond answer\n"+
		"\nContinue this conversation from here. The user's next message follows.\n\nthird question", got)
	assert.Less(t, strings.Index(got, "first answer"), strings.Index(got, "second answer"), "turn order preserved")
}

func TestBuildRebuildPrompt_OversizedIsRestorationErrorNotTruncation(t *testing.T) {
	conv := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": strings.Repeat("x", 1000)},
		map[string]any{"role": "assistant", "content": "y"},
	}}
	got, err := engine.BuildRebuildPrompt(conv, "next", 500)
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrRebuildTooLarge))
	assert.Empty(t, got, "never return a truncated prompt")
}

func TestBuildRebuildPrompt_NoFirstPromptFails(t *testing.T) {
	_, err := engine.BuildRebuildPrompt(map[string]any{}, "next", 0)
	assert.True(t, errors.Is(err, engine.ErrSessionRestoreFailed))
}

func TestRebuildLimitBytes_TracksContextWindow(t *testing.T) {
	assert.Equal(t, 480_000, engine.RebuildLimitBytes(provider.ContextDefault))
	assert.Equal(t, 480_000, engine.RebuildLimitBytes(provider.Context200K))
	assert.Equal(t, 2_400_000, engine.RebuildLimitBytes(provider.Context1M))
}

func TestSessionWaitError_IsNoSessionTarget(t *testing.T) {
	reset := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	err := error(&engine.SessionWaitError{Kind: "capacity", Group: "anthropic_subscription:anthropic", Model: "claude-opus-4-5", NextReset: &reset})
	assert.True(t, errors.Is(err, engine.ErrNoSessionTarget))
	assert.Contains(t, err.Error(), "anthropic_subscription:anthropic")
	assert.Contains(t, err.Error(), "2026-11-20T12:00:00Z")
}
