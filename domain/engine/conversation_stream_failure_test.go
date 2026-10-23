package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without the terminal-Done guard, a truncated turn (even one containing the
// completion sentinel) overwrites the durable conversation and can end the run.
func TestConversationHandler_HandleMessage_TruncatedStreamPreservesConversation(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"empty stream", ""},
		{"partial response", "unfinished response"},
		{"completion sentinel without Done", `<arbitron:result:test-nonce-fixed>{"summary":"premature"}</arbitron:result:test-nonce-fixed>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
			before, err := nodeExecRepo.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			conversation := before.LLMConversation
			llm.streamCallFn = func(_ context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
				require.Equal(t, interactiveChatModel, req.Model)
				require.True(t, req.RequireExecutedModel)
				ch := make(chan engine.ChatChunk, 1)
				if tc.text != "" {
					ch <- engine.ChatChunk{Token: tc.text, SessionID: "unconfirmed-session"}
				}
				close(ch)
				return ch, nil
			}
			handler := engine.NewConversationHandler(instRepo, nodeExecRepo, streamSelector(llm), pinnedResolver{interactiveResolver()}, bus, queue)
			var releases int
			handler.(interface{ SetPinCacheRelease(func(uuid.UUID)) }).SetPinCacheRelease(func(uuid.UUID) { releases++ })
			err = handler.HandleMessage(convoCtx(), id, "follow-up")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "without completion")
			after, err := nodeExecRepo.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			assert.Equal(t, conversation, after.LLMConversation)
			assert.Nil(t, after.Output)
			assert.Equal(t, instance.StateWaiting, instRepo.get(id).State)
			assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveEnded))
			assert.Zero(t, releases, "uncompleted output must not release the pin cache")
			for _, event := range bus.eventsOfType(id, engine.EventTypeInteractiveMessage) {
				assert.Equal(t, "user", event.Data.(map[string]any)["role"])
			}

			// A failed turn releases the operation guard and permits a verified retry.
			llm.streamCallFn = cannedStreamWithSession([]string{"verified retry"}, "confirmed-session")
			require.NoError(t, handler.HandleMessage(convoCtx(), id, "retry"))
			after, err = nodeExecRepo.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			assert.Len(t, extractMessages(t, after.LLMConversation), 4)
			assert.Equal(t, "confirmed-session", engine.SessionID(after.LLMConversation))
		})
	}
}

// Cancellation must interrupt a provider that never closes its stream. A nil
// channel must also fail promptly rather than blocking forever.
func TestConversationHandler_HandleMessage_CancelledOrNilStreamRefused(t *testing.T) {
	for _, tc := range []struct {
		name      string
		nilStream bool
		deadline  bool
	}{
		{name: "cancelled stalled stream"},
		{name: "timed out stalled stream", deadline: true},
		{name: "nil stream", nilStream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instRepo, nodeExecRepo, llm, bus, queue, id := conversationTestSetup(t)
			ctx, cancel := context.WithCancel(convoCtx())
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(convoCtx(), 20*time.Millisecond)
			}
			defer cancel()
			ch := make(chan engine.ChatChunk, 1)
			defer close(ch)
			llm.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
				if tc.nilStream {
					return nil, nil
				}
				ch <- engine.ChatChunk{Token: "partial"}
				if tc.deadline {
					<-ctx.Done()
				} else {
					cancel()
				}
				return ch, nil
			}
			handler := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
			result := make(chan error, 1)
			go func() { result <- handler.HandleMessage(ctx, id, "follow-up") }()
			select {
			case err := <-result:
				require.Error(t, err)
				if !tc.nilStream {
					assert.ErrorIs(t, err, ctx.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("continuation did not refuse the cancelled or nil stream")
			}
			exec, err := nodeExecRepo.GetByInstanceAndNode(convoCtx(), id, "interact")
			require.NoError(t, err)
			assert.Len(t, extractMessages(t, exec.LLMConversation), 2)
			assert.Equal(t, instance.StateWaiting, instRepo.get(id).State)
		})
	}
}
