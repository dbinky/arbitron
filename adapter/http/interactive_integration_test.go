package http_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Helper: set up an instance in interactive wait state with conversation
// history and a workflow snapshot containing an interactive node.
// ---------------------------------------------------------------------------

func interactiveWaitInstance() (*instance.Instance, *instance.NodeExecution) {
	nodeID := "review-design"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{
				"id":   "review-design",
				"type": "interactive",
				"config": map[string]any{
					"systemPrompt": "You are a reviewer.",
					"model":        "claude-sonnet-4-20250514",
					"maxTokens":    4096,
					"temperature":  0.7,
					"outputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"approved": map[string]any{"type": "boolean"},
						},
					},
				},
			},
		},
	}

	nodeExec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: inst.ID,
		NodeID:     "review-design",
		Attempt:    1,
		Status:     instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "Please review the design"},
				map[string]any{"role": "assistant", "content": "I'll review the design for you."},
			},
		},
		StartedAt: time.Now(),
	}

	return inst, nodeExec
}

// readWSEvent reads the next WebSocket message and unmarshals it into a map.
func readWSEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(raw, &evt))
	return evt
}

// ===========================================================================
// Integration Test: Full message round-trip via WebSocket
// ===========================================================================

func TestIntegration_InteractiveRoundTrip_FullFlow(t *testing.T) {
	// Setup: instance in interactive wait state with conversation history.
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	inst, nodeExec := interactiveWaitInstance()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{nodeExec}

	// The mock ConversationHandler simulates the real handler's behavior:
	// 1. HandleMessage publishes user message, token events, and assistant message
	// 2. EndSession publishes interactive.ended with reason user_ended and
	//    transitions to queued
	convo := &mockConversationHandler{
		handleMessageFn: func(_ context.Context, instanceID uuid.UUID, content string) error {
			// Publish user message event.
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveMessage,
				Timestamp: time.Now(),
				Data:      map[string]any{"role": "user", "content": content},
			})
			// Stream tokens.
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": "I'll "},
			})
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": "review that."},
			})
			// Publish assistant message event.
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveMessage,
				Timestamp: time.Now(),
				Data:      map[string]any{"role": "assistant", "content": "I'll review that."},
			})
			return nil
		},
		endSessionFn: func(_ context.Context, instanceID uuid.UUID) error {
			// Transition instance to queued (simulating real handler).
			instRepo.instances[instanceID].State = instance.StateQueued
			// Publish interactive.ended event.
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveEnded,
				Timestamp: time.Now(),
				Data: map[string]any{
					"reason": "user_ended",
					"output": map[string]any{},
				},
			})
			return nil
		},
	}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Step 1: Verify interactive.history is received with initial conversation.
	historyEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveHistory, historyEvt["type"])
	historyData, ok := historyEvt["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "review-design", historyData["nodeId"])
	messages, ok := historyData["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 2, "should receive 2 initial messages in history")

	// Step 2: Send interactive.send message.
	time.Sleep(50 * time.Millisecond) // ensure subscription is ready
	sendMsg := `{"type":"interactive.send","content":"Looks good to me"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(sendMsg))
	require.NoError(t, err)

	// Step 3: Verify user message event.
	userMsgEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveMessage, userMsgEvt["type"])
	userMsgData, ok := userMsgEvt["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", userMsgData["role"])
	assert.Equal(t, "Looks good to me", userMsgData["content"])

	// Step 4: Verify token events flow during LLM streaming.
	token1 := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveToken, token1["type"])
	token1Data, ok := token1["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "I'll ", token1Data["token"])

	token2 := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveToken, token2["type"])
	token2Data, ok := token2["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "review that.", token2Data["token"])

	// Step 5: Verify assistant message event.
	assistantEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveMessage, assistantEvt["type"])
	assistantData, ok := assistantEvt["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", assistantData["role"])
	assert.Equal(t, "I'll review that.", assistantData["content"])

	// Step 6: Send interactive.end.
	endMsg := `{"type":"interactive.end"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(endMsg))
	require.NoError(t, err)

	// Step 7: Verify interactive.ended event flows back.
	endedEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveEnded, endedEvt["type"])
	endedData, ok := endedEvt["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user_ended", endedData["reason"])

	// Step 8: Verify the ConversationHandler received the correct calls.
	convo.mu.Lock()
	require.Len(t, convo.handleCalls, 1)
	assert.Equal(t, inst.ID, convo.handleCalls[0].InstanceID)
	assert.Equal(t, "Looks good to me", convo.handleCalls[0].Content)
	require.Len(t, convo.endCalls, 1)
	assert.Equal(t, inst.ID, convo.endCalls[0])
	convo.mu.Unlock()

	// Step 9: Verify instance transitioned to queued.
	assert.Equal(t, instance.StateQueued, instRepo.instances[inst.ID].State)
}

// ===========================================================================
// Integration Test: Tool completion (LLM invokes complete_session)
// ===========================================================================

func TestIntegration_InteractiveToolCompletion_LLMCompleted(t *testing.T) {
	// Setup: instance in interactive wait state.
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	inst, nodeExec := interactiveWaitInstance()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{nodeExec}

	structuredOutput := map[string]any{"approved": true}

	// Mock ConversationHandler simulates the LLM calling complete_session.
	convo := &mockConversationHandler{
		handleMessageFn: func(_ context.Context, instanceID uuid.UUID, content string) error {
			// Publish user message event.
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveMessage,
				Timestamp: time.Now(),
				Data:      map[string]any{"role": "user", "content": content},
			})
			// Stream tokens for the assistant's thinking.
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": "Completing session..."},
			})
			// The LLM invokes complete_session — handler transitions to queued
			// and stores structured output.
			instRepo.instances[instanceID].State = instance.StateQueued

			// Store output on node execution.
			for _, exec := range nodeExecRepo.executions {
				if exec.InstanceID == instanceID {
					exec.Output = structuredOutput
					break
				}
			}

			// Publish interactive.ended with reason llm_completed.
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveEnded,
				Timestamp: time.Now(),
				Data: map[string]any{
					"reason": "llm_completed",
					"output": structuredOutput,
				},
			})
			return nil
		},
	}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Step 1: Verify interactive.history.
	historyEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveHistory, historyEvt["type"])

	// Step 2: Send a message that triggers LLM tool completion.
	time.Sleep(50 * time.Millisecond)
	sendMsg := `{"type":"interactive.send","content":"Please approve the design"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(sendMsg))
	require.NoError(t, err)

	// Step 3: Verify user message event.
	userEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveMessage, userEvt["type"])
	userData, ok := userEvt["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", userData["role"])

	// Step 4: Verify token event.
	tokenEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveToken, tokenEvt["type"])

	// Step 5: Verify interactive.ended with reason llm_completed.
	endedEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveEnded, endedEvt["type"])
	endedData, ok := endedEvt["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "llm_completed", endedData["reason"])

	// Verify structured output in the ended event.
	outputData, ok := endedData["output"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, outputData["approved"])

	// Step 6: Verify instance transitioned to queued.
	assert.Equal(t, instance.StateQueued, instRepo.instances[inst.ID].State)

	// Step 7: Verify node execution record has the structured output.
	for _, exec := range nodeExecRepo.executions {
		if exec.InstanceID == inst.ID {
			require.NotNil(t, exec.Output)
			assert.Equal(t, true, exec.Output["approved"])
		}
	}
}

// ===========================================================================
// Integration Test: History not sent for non-interactive wait state
// ===========================================================================

func TestIntegration_InteractiveHistory_OnlyForInteractiveWait(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	// Instance is running, not in interactive wait.
	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	convo := &mockConversationHandler{}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler time to send history (which it should NOT do).
	time.Sleep(100 * time.Millisecond)

	// Publish a bus event to verify no history preceded it.
	bus.Publish(inst.ID, engine.Event{
		Type:      engine.EventTypeNodeStarted,
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	evt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeNodeStarted, evt["type"],
		"first event should be node.started, not interactive.history")
}

// ===========================================================================
// Integration Test: Error during HandleMessage flows back as interactive.error
// ===========================================================================

func TestIntegration_InteractiveError_FlowsBackOverWebSocket(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	inst, nodeExec := interactiveWaitInstance()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{nodeExec}

	convo := &mockConversationHandler{
		handleMessageFn: func(_ context.Context, instanceID uuid.UUID, _ string) error {
			return assert.AnError
		},
	}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Consume the history event first.
	historyEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveHistory, historyEvt["type"])

	// Send a message that will cause the handler to error.
	time.Sleep(50 * time.Millisecond)
	sendMsg := `{"type":"interactive.send","content":"trigger error"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(sendMsg))
	require.NoError(t, err)

	// Verify interactive.error is received.
	errEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveError, errEvt["type"])
	errData, ok := errEvt["data"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, errData["message"])
}

// ===========================================================================
// Integration Test: Multiple send/receive cycles work correctly
// ===========================================================================

func TestIntegration_InteractiveMultipleMessages(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	inst, nodeExec := interactiveWaitInstance()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{nodeExec}

	msgCount := 0
	convo := &mockConversationHandler{
		handleMessageFn: func(_ context.Context, instanceID uuid.UUID, content string) error {
			msgCount++
			// Publish just an assistant message event (no tokens, to keep test simple).
			bus.Publish(instanceID, engine.Event{
				Type:      engine.EventTypeInteractiveMessage,
				Timestamp: time.Now(),
				Data: map[string]any{
					"role":    "assistant",
					"content": "Response " + content,
				},
			})
			return nil
		},
	}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Consume history.
	historyEvt := readWSEvent(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveHistory, historyEvt["type"])

	time.Sleep(50 * time.Millisecond)

	// Send 3 messages and verify each gets a response.
	for i := 0; i < 3; i++ {
		sendMsg := `{"type":"interactive.send","content":"msg` + string(rune('1'+i)) + `"}`
		err = conn.Write(ctx, websocket.MessageText, []byte(sendMsg))
		require.NoError(t, err)

		// Read assistant response.
		assistantEvt := readWSEvent(t, ctx, conn)
		assert.Equal(t, engine.EventTypeInteractiveMessage, assistantEvt["type"])
		data, ok := assistantEvt["data"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "assistant", data["role"])
	}

	// Verify all 3 messages were handled.
	convo.mu.Lock()
	assert.Len(t, convo.handleCalls, 3)
	convo.mu.Unlock()
}
