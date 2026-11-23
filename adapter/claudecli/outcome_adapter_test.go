package claudecli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	pool "github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutcomeAdapter_PinnedResultErrorIsRedacted(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "collect", true: "stream"}[streaming], func(t *testing.T) {
			fr := &fakeRunner{lines: fixtureLines(t, filepath.Join("testdata", "outcomes", "usage_limit_seven_day.jsonl"))}
			a := NewChatAdapter(Config{Runner: fr})
			req := engine.ChatRequest{WorkDir: t.TempDir(), Model: "claude-sonnet-4-5", RequireExecutedModel: true}
			var err error
			if streaming {
				ch, e := a.StreamChat(context.Background(), req)
				require.NoError(t, e)
				for c := range ch {
					if c.Error != nil {
						err = c.Error
					}
				}
			} else {
				_, err = a.Chat(context.Background(), req)
			}
			require.Error(t, err)
			var oe *pool.OutcomeError
			require.True(t, errors.As(err, &oe))
			assert.Equal(t, pool.OutcomeExhausted, oe.Outcome.Class)
			assert.Equal(t, "harness process failed", oe.Err.Error())
			var se *StderrError
			assert.False(t, errors.As(err, &se))
		})
	}
}

func TestOutcomeAdapter_HeadroomReachesObserver(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "collect", true: "stream"}[streaming], func(t *testing.T) {
			var seen []pool.SourceOutcome
			ctx := pool.ContextWithOutcomeObserver(context.Background(), func(o pool.SourceOutcome) { seen = append(seen, o) })
			a := NewChatAdapter(Config{Runner: &fakeRunner{lines: fixtureLines(t, filepath.Join("testdata", "outcomes", "allowed_warning.jsonl"))}})
			req := engine.ChatRequest{WorkDir: t.TempDir(), Model: "claude-sonnet-4-5", RequireExecutedModel: true}
			if streaming {
				ch, err := a.StreamChat(ctx, req)
				require.NoError(t, err)
				for c := range ch {
					require.NoError(t, c.Error)
				}
			} else {
				_, err := a.Chat(ctx, req)
				require.NoError(t, err)
			}
			require.Len(t, seen, 1)
			require.NotNil(t, seen[0].HeadroomHint)
			assert.InDelta(t, 0.15, *seen[0].HeadroomHint, 1e-9)
		})
	}
}
