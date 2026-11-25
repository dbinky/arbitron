package sources

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

type scriptedConfirm struct {
	mu    sync.Mutex
	calls []string
	out   map[string]runner.ConfirmationResult
	errs  map[string]error
}

func (s *scriptedConfirm) Confirm(_ context.Context, b provider.ResolvedBinding, workDir string) (runner.ConfirmationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, b.Model+"@"+b.Provider+"@"+workDir)
	if err := s.errs[b.Model]; err != nil {
		return runner.ConfirmationResult{}, err
	}
	return s.out[b.Model], nil
}

func okResult(m string) runner.ConfirmationResult {
	return runner.ConfirmationResult{RequestedModel: m, ExecutedModel: m, SessionID: "s-" + m}
}

func TestClaudeModelVerifier_AdvertisesOnlyNativelyVerifiedRequestedModels(t *testing.T) {
	c := &scriptedConfirm{out: map[string]runner.ConfirmationResult{
		"claude-opus-5-5":   okResult("claude-opus-5-5"),
		"claude-sonnet-4-5": {RequestedModel: "claude-sonnet-4-5", ExecutedModel: "claude-haiku-4-5", SessionID: "s"},
	}}
	v := NewClaudeModelVerifier(c, "/scratch", nil)
	v.Request(context.Background(), []string{"claude-sonnet-4-5", "claude-opus-5-5"})
	assert.Equal(t, []string{"claude-opus-5-5"}, v.Verified(), "a mismatched executed model is never advertised")
	assert.ElementsMatch(t, []string{"claude-opus-5-5@claude-cli@/scratch", "claude-sonnet-4-5@claude-cli@/scratch"}, c.calls)
}

func TestClaudeModelVerifier_CachesVerdictsButRetriesTransientErrors(t *testing.T) {
	c := &scriptedConfirm{
		out:  map[string]runner.ConfirmationResult{"claude-opus-5-5": okResult("claude-opus-5-5"), "claude-sonnet-4-5": {RequestedModel: "claude-sonnet-4-5", ExecutedModel: "x", SessionID: "s"}},
		errs: map[string]error{"claude-haiku-4-5": errors.New("rate limited")},
	}
	v := NewClaudeModelVerifier(c, "/scratch", nil)
	all := []string{"claude-opus-5-5", "claude-sonnet-4-5", "claude-haiku-4-5"}
	v.Request(context.Background(), all)
	require.Len(t, c.calls, 3)
	v.Request(context.Background(), all)
	assert.Len(t, c.calls, 4, "only the transient failure is retried; verified and mismatched verdicts are cached")
	assert.Equal(t, "claude-haiku-4-5@claude-cli@/scratch", c.calls[3])
}

func TestClaudeModelVerifier_VerifiedIsScopedToTheLatestRequest(t *testing.T) {
	c := &scriptedConfirm{out: map[string]runner.ConfirmationResult{"claude-opus-5-5": okResult("claude-opus-5-5"), "claude-sonnet-4-5": okResult("claude-sonnet-4-5")}}
	v := NewClaudeModelVerifier(c, "/scratch", nil)
	v.Request(context.Background(), []string{"claude-opus-5-5", "claude-sonnet-4-5"})
	v.Request(context.Background(), []string{"claude-sonnet-4-5"})
	assert.Equal(t, []string{"claude-sonnet-4-5"}, v.Verified(), "a model the server no longer needs is not advertised")
	v.Request(context.Background(), nil)
	assert.Empty(t, v.Verified())
}

func TestClaudeModelVerifier_CancelledSessionStopsAndCachesNothing(t *testing.T) {
	c := &scriptedConfirm{errs: map[string]error{"claude-opus-5-5": context.Canceled}}
	v := NewClaudeModelVerifier(c, "/scratch", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v.Request(ctx, []string{"claude-opus-5-5", "claude-sonnet-4-5"})
	assert.Empty(t, v.Verified())
	assert.Empty(t, c.calls, "a cancelled admission spends no tokens")
}
