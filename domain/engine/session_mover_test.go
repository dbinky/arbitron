package engine_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishTurnRestartedAndContextNotice_MapPayloads(t *testing.T) {
	bus := newMockEventBus()
	run := uuid.New()
	turn := engine.InteractiveTurnIdentity{RunID: run, NodeID: "talk", InvocationID: uuid.New(), RequestID: uuid.New(), TurnIndex: 3, Generation: 7}
	engine.PublishTurnRestarted(bus, turn)
	engine.PublishContextNotice(bus, turn, engine.SessionLossNotice)
	restarted := bus.eventsOfType(run, engine.EventTypeInteractiveTurnRestarted)
	require.Len(t, restarted, 1)
	assert.False(t, restarted[0].Timestamp.IsZero())
	assert.Equal(t, map[string]any{"instanceId": run.String(), "nodeId": "talk", "invocationId": turn.InvocationID.String(), "requestId": turn.RequestID.String(), "turnIndex": 3, "generation": int64(7)}, restarted[0].Data)
	notice := bus.eventsOfType(run, engine.EventTypeInteractiveContextNotice)
	require.Len(t, notice, 1)
	assert.False(t, notice[0].Timestamp.IsZero())
	assert.Equal(t, map[string]any{"instanceId": run.String(), "nodeId": "talk", "invocationId": turn.InvocationID.String(), "requestId": turn.RequestID.String(), "turnIndex": 3, "generation": int64(7), "notice": "tool details and reasoning may be lost", "lossy": true}, notice[0].Data)
}

func TestPublishTurnRestartedAndContextNotice_RejectIncompleteIdentity(t *testing.T) {
	valid := engine.InteractiveTurnIdentity{RunID: uuid.New(), NodeID: "talk", InvocationID: uuid.New(), RequestID: uuid.New(), TurnIndex: 3, Generation: 7}
	for _, field := range []string{"run", "node", "invocation", "request", "turn", "generation"} {
		t.Run(field, func(t *testing.T) {
			turn := valid
			switch field {
			case "run":
				turn.RunID = uuid.Nil
			case "node":
				turn.NodeID = ""
			case "invocation":
				turn.InvocationID = uuid.Nil
			case "request":
				turn.RequestID = uuid.Nil
			case "turn":
				turn.TurnIndex = -1
			case "generation":
				turn.Generation = 0
			}
			bus := newMockEventBus()
			engine.PublishTurnRestarted(bus, turn)
			engine.PublishContextNotice(bus, turn, engine.SessionLossNotice)
			assert.Empty(t, bus.eventsOfType(turn.RunID, engine.EventTypeInteractiveTurnRestarted))
			assert.Empty(t, bus.eventsOfType(turn.RunID, engine.EventTypeInteractiveContextNotice))
		})
	}
}

// Exercise the existing accepted-intent identity producer and real subscriber
// sink. Composition-root mover/replay acceptance is owned by the later cycles.
func TestPublishTurnRestartedAndContextNotice_AcceptedIntentReachesSubscriber(t *testing.T) {
	bus := eventbus.New()
	intent := engine.TurnIntent{RunID: uuid.New(), NodeID: "talk", InvocationID: uuid.New(), RequestID: uuid.New(), TurnIndex: 4, SessionGeneration: 9}
	events, unsubscribe := bus.Subscribe(intent.RunID)
	defer unsubscribe()
	turn := engine.TurnIdentityFromIntent(intent)
	engine.PublishTurnRestarted(bus, turn)
	engine.PublishContextNotice(bus, turn, engine.SessionLossNotice)
	for _, kind := range []string{engine.EventTypeInteractiveTurnRestarted, engine.EventTypeInteractiveContextNotice} {
		select {
		case event := <-events:
			require.Equal(t, kind, event.Type)
			data, ok := event.Data.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, intent.RunID.String(), data["instanceId"])
			assert.Equal(t, intent.NodeID, data["nodeId"])
			assert.Equal(t, intent.InvocationID.String(), data["invocationId"])
			assert.Equal(t, intent.RequestID.String(), data["requestId"])
			assert.Equal(t, intent.TurnIndex, data["turnIndex"])
			assert.Equal(t, intent.SessionGeneration, data["generation"])
			if kind == engine.EventTypeInteractiveContextNotice {
				assert.Equal(t, engine.SessionLossNotice, data["notice"])
				assert.Equal(t, true, data["lossy"])
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber did not receive %s", kind)
		}
	}
}

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
