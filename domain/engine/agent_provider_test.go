package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAgent is an in-package fake satisfying engine.AgentProvider. It lets us
// exercise the new port surface without a real subprocess-backed adapter (the
// claude-cli AgentAdapter lands in a later task).
type fakeAgent struct {
	runFn func(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error)
}

func (f *fakeAgent) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	return f.runFn(ctx, mission)
}

// Compile-time assertion: a fake can satisfy the new AgentProvider port.
var _ engine.AgentProvider = (*fakeAgent)(nil)

// Happy path: a fake implementing engine.AgentProvider is accepted through the
// port and RunAgent returns a populated result for a normal mission.
func TestAgentProvider_HappyPath(t *testing.T) {
	var provider engine.AgentProvider = &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			return &engine.AgentResult{
				Summary: "applied edits for " + m.Prompt,
				Model:   m.Model,
			}, nil
		},
	}

	res, err := provider.RunAgent(context.Background(), engine.AgentMission{
		Model:  "claude-opus-4-8",
		Prompt: "fix the bug",
	})

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "applied edits for fix the bug", res.Summary)
	assert.Equal(t, "claude-opus-4-8", res.Model)
}

// Success: a full AgentMission round-trips all fields into the fake and an
// AgentResult round-trips all fields back, including step events.
func TestAgentProvider_RoundTripsAllFields(t *testing.T) {
	mission := engine.AgentMission{
		Model:     "claude-opus-4-8",
		Prompt:    "refactor the parser",
		WorkDir:   "/workspace/inst-1",
		ToolScope: []string{"Read", "Edit", "Bash(go test:*)"},
		SessionID: "sess-42",
		Timeout:   30 * time.Second,
	}

	var captured engine.AgentMission
	provider := &fakeAgent{
		runFn: func(_ context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
			captured = m
			return &engine.AgentResult{
				Summary: "done",
				Steps: []engine.AgentStep{
					{Kind: "tool_call", Summary: "Read parser.go"},
					{Kind: "edit", Summary: "rewrote tokenizer"},
					{Kind: "result", Summary: "tests pass"},
				},
				Model:     m.Model,
				TokensIn:  120,
				TokensOut: 480,
				SessionID: m.SessionID,
			}, nil
		},
	}

	res, err := provider.RunAgent(context.Background(), mission)
	require.NoError(t, err)

	// Mission fields propagate unchanged.
	assert.Equal(t, mission, captured)

	// Result fields are populated and readable.
	assert.Equal(t, "done", res.Summary)
	assert.Equal(t, "claude-opus-4-8", res.Model)
	assert.Equal(t, 120, res.TokensIn)
	assert.Equal(t, 480, res.TokensOut)
	assert.Equal(t, "sess-42", res.SessionID)
	require.Len(t, res.Steps, 3)
	assert.Equal(t, "tool_call", res.Steps[0].Kind)
	assert.Equal(t, "rewrote tokenizer", res.Steps[1].Summary)
}

// Failure: a domain-level failure reported by the agent backend (a non-nil
// error and a nil result) surfaces through the port to the caller.
func TestAgentProvider_FailurePropagates(t *testing.T) {
	wantErr := errors.New("mission failed: agent reported error")
	provider := &fakeAgent{
		runFn: func(_ context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			return nil, wantErr
		},
	}

	res, err := provider.RunAgent(context.Background(), engine.AgentMission{Prompt: "x"})

	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, res)
}

// Error: a cancelled context is honoured — the port forwards the cancellation
// (context-shaped error) to the caller rather than swallowing it.
func TestAgentProvider_ContextCancellation(t *testing.T) {
	provider := &fakeAgent{
		runFn: func(ctx context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			return nil, ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := provider.RunAgent(ctx, engine.AgentMission{Prompt: "x"})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, res)
}

// Edge case: zero-value-friendly shapes. An AgentMission with an empty
// ToolScope and zero Timeout, and an AgentResult with a nil Steps slice, are
// valid and carry their zero-value field defaults.
func TestAgentProvider_ZeroValueShapes(t *testing.T) {
	t.Run("mission with empty ToolScope and zero Timeout", func(t *testing.T) {
		m := engine.AgentMission{
			Model:  "m",
			Prompt: "p",
		}
		assert.Nil(t, m.ToolScope)
		assert.Equal(t, time.Duration(0), m.Timeout)
		assert.Empty(t, m.SessionID)
		assert.Empty(t, m.WorkDir)
	})

	t.Run("result with nil Steps", func(t *testing.T) {
		r := engine.AgentResult{Summary: "ok"}
		assert.Nil(t, r.Steps)
		assert.Equal(t, 0, r.TokensIn)
		assert.Equal(t, 0, r.TokensOut)
		assert.Empty(t, r.Model)
		assert.Empty(t, r.SessionID)
	})

	t.Run("zero-value AgentStep", func(t *testing.T) {
		var s engine.AgentStep
		assert.Empty(t, s.Kind)
		assert.Empty(t, s.Summary)
	})
}
