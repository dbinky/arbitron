package runner_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithOutcome_WrapsAndUnwraps(t *testing.T) {
	assert.NoError(t, runner.WithOutcome(nil, runner.SourceOutcome{Class: runner.OutcomeExhausted}), "nil error stays nil")

	base := errors.New("claude exited 1")
	reset := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	err := runner.WithOutcome(base, runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset})
	assert.Equal(t, "claude exited 1", err.Error(), "the outcome adds no text of its own")
	assert.ErrorIs(t, err, base)

	var oe *runner.OutcomeError
	require.True(t, errors.As(fmt.Errorf("adapter: %w", err), &oe), "survives further wrapping")
	assert.Equal(t, runner.OutcomeExhausted, oe.Outcome.Class)
	assert.Equal(t, &reset, oe.Outcome.ResetAt)
}

func TestClassifyError_Table(t *testing.T) {
	reset := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		want runner.OutcomeClass
	}{
		{"nil", nil, runner.OutcomeNone},
		{"exhausted outcome", runner.WithOutcome(errors.New("x"), runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset}), runner.OutcomeExhausted},
		{"logged out outcome", runner.WithOutcome(errors.New("x"), runner.SourceOutcome{Class: runner.OutcomeLoggedOut}), runner.OutcomeLoggedOut},
		{"transient outcome", runner.WithOutcome(errors.New("x"), runner.SourceOutcome{Class: runner.OutcomeTransient}), runner.OutcomeTransient},
		{"runner lost sentinel", fmt.Errorf("hub: %w", runner.ErrRunnerLost), runner.OutcomeRunnerLost},
		{"plain error", errors.New("exit status 2"), runner.OutcomeOther},
		{"context canceled", context.Canceled, runner.OutcomeOther},
		{"outcome with empty class fails closed", runner.WithOutcome(errors.New("x"), runner.SourceOutcome{}), runner.OutcomeOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, runner.ClassifyError(c.err).Class)
		})
	}
}

func TestClassifyError_LunaRouteBusyIsTransientNeverExhausted(t *testing.T) {
	busy := fmt.Errorf("agent: %w", &provider.LunaRouteFailure{Kind: provider.ErrLunaRouteBusy, Operation: provider.LunaRouteChat, Status: 429, RetryAfter: "7"})
	o := runner.ClassifyError(busy)
	assert.Equal(t, runner.OutcomeTransient, o.Class)
	assert.Equal(t, 7*time.Second, o.RetryAfter)
	assert.NotEqual(t, runner.OutcomeExhausted, o.Class)

	bare := runner.ClassifyError(provider.ErrLunaRouteBusy)
	assert.Equal(t, runner.OutcomeTransient, bare.Class)
	assert.Zero(t, bare.RetryAfter, "no Retry-After known")

	date := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	dated := runner.ClassifyError(&provider.LunaRouteFailure{Kind: provider.ErrLunaRouteBusy, RetryAfter: date})
	assert.InDelta(t, float64(90*time.Second), float64(dated.RetryAfter), float64(2*time.Second))

	past := runner.ClassifyError(&provider.LunaRouteFailure{Kind: provider.ErrLunaRouteBusy, RetryAfter: "Mon, 02 Jan 2006 15:04:05 GMT"})
	assert.Zero(t, past.RetryAfter, "a past date never yields a negative wait")

	other := runner.ClassifyError(&provider.LunaRouteFailure{Kind: provider.ErrLunaRouteModelUnavailable})
	assert.Equal(t, runner.OutcomeOther, other.Class, "only busy is transient")
}

func TestClassifyError_NeverReadsProse(t *testing.T) {
	ascii := "You've hit your usage limit. Upgrade to Pro (https://chatgpt.com/settings/usage) or try again at 6:00 PM."
	curly := "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/settings/usage) or try again at 6:00 PM."
	require.NotEqual(t, ascii, curly)

	// Bare prose: identical, and never exhaustion.
	a := runner.ClassifyError(errors.New(ascii))
	c := runner.ClassifyError(errors.New(curly))
	assert.Equal(t, a, c)
	assert.Equal(t, runner.OutcomeOther, a.Class, "prose alone never parks a source")

	// Structured outcome: the class comes only from the structure.
	reset := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	o := runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset}
	assert.Equal(t, runner.ClassifyError(runner.WithOutcome(errors.New(ascii), o)), runner.ClassifyError(runner.WithOutcome(errors.New(curly), o)))

	// Prose that SAYS "logged out" but carries no structure is still other.
	assert.Equal(t, runner.OutcomeOther, runner.ClassifyError(errors.New("authentication_failed: please run /login")).Class)
}
