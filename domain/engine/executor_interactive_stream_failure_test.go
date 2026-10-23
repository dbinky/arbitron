package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cancelOnInteractiveToken struct {
	engine.EventBus
	cancel context.CancelFunc
}

func (b cancelOnInteractiveToken) Publish(id uuid.UUID, event engine.Event) {
	b.EventBus.Publish(id, event)
	if event.Type == engine.EventTypeInteractiveToken {
		b.cancel()
	}
}

// Removing the nil guard or cancellation checks must either trip the watchdog
// or incorrectly persist a cancelled first turn as a successful conversation.
func TestInteractiveExecutor_InvalidFirstTurnStreamRefusesWaiting(t *testing.T) {
	for _, tc := range []struct {
		name          string
		nilStream     bool
		deadline      bool
		bufferedDone  bool
		cancelOnToken bool
	}{
		{name: "nil stream", nilStream: true},
		{name: "cancelled stalled stream"},
		{name: "timed out stalled stream", deadline: true},
		{name: "buffered Done after cancellation", bufferedDone: true},
		{name: "buffered Done after deadline", bufferedDone: true, deadline: true},
		{name: "cancellation while publishing terminal token", bufferedDone: true, cancelOnToken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instRepo, nodeExecRepo, llm, files, bus, queue, id := interactiveTestSetup()
			files.files["test-wf/orchestration.md"] = []byte("Orchestration")
			files.files["test-wf/template.md"] = []byte("Template")
			baseCtx := engine.ContextWithInstanceID(context.Background(), id)
			ctx, cancel := context.WithCancel(baseCtx)
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(baseCtx, 50*time.Millisecond)
			}
			defer cancel()
			var events engine.EventBus = bus
			if tc.cancelOnToken {
				events = cancelOnInteractiveToken{EventBus: bus, cancel: cancel}
			}
			ch := make(chan engine.ChatChunk, 1)
			defer close(ch)
			llm.streamCallFn = func(_ context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
				if req.Model != interactiveChatModel || !req.RequireExecutedModel {
					return nil, assert.AnError
				}
				if tc.nilStream {
					return nil, nil
				}
				ch <- engine.ChatChunk{Token: "partial response", Done: tc.bufferedDone, SessionID: "unconfirmed-session"}
				if tc.bufferedDone && !tc.cancelOnToken {
					if tc.deadline {
						<-ctx.Done()
					} else {
						cancel()
					}
				} else if !tc.deadline && !tc.cancelOnToken {
					// Let Execute receive the partial token before cancelling, but
					// deliberately leave the provider's stream open afterwards.
					go func() {
						ch <- engine.ChatChunk{SessionID: "still-streaming"}
						cancel()
					}()
				}
				return ch, nil
			}
			executor := engine.NewInteractiveExecutor(instRepo, nodeExecRepo, streamSelector(llm), pinnedResolver{interactiveResolver()}, files, events, queue)
			type outcome struct {
				result *engine.NodeResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
				done <- outcome{result, err}
			}()
			select {
			case got := <-done:
				require.Error(t, got.err)
				assert.Nil(t, got.result, "a failed stream must not yield a waiting result")
				if tc.nilStream {
					assert.Contains(t, got.err.Error(), "nil stream")
				} else if tc.deadline {
					assert.ErrorIs(t, got.err, context.DeadlineExceeded)
				} else {
					assert.ErrorIs(t, got.err, context.Canceled)
				}
			case <-time.After(time.Second):
				t.Fatal("first-turn reader did not refuse the nil or cancelled stream")
			}
			execs, err := nodeExecRepo.ListByInstance(baseCtx, id)
			require.NoError(t, err)
			assert.Empty(t, execs, "unconfirmed history must not be persisted")
			assert.Equal(t, instance.StateRunning, instRepo.get(id).State)
			assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveMessage))
			assert.Zero(t, queue.notifyCount())
		})
	}
}
