package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers for conversation handler tests
// ---------------------------------------------------------------------------

// conversationTestSetup creates the standard mock dependencies and an instance
// in the "waiting" state on an interactive node, with an existing node execution
// record containing the initial conversation.
func conversationTestSetup(t *testing.T) (
	*mockInstanceRepo,
	*mockNodeExecutionRepo,
	*mockStreamingLLMProvider,
	*mockEventBus,
	*mockQueueManager,
	uuid.UUID,
) {
	t.Helper()

	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingLLMProvider()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	instanceID := uuid.New()
	nodeID := "interact"

	// Build the workflow snapshot with an interactive node.
	wf := workflow.Workflow{
		Name: "test-wf",
		Nodes: []workflow.Node{
			{
				ID:         nodeID,
				Type:       workflow.NodeTypeInteractive,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					SystemPrompt: "You are a helpful assistant.",
					Model:        "claude-sonnet-4-20250514",
					MaxTokens:    4096,
					Temperature:  0.7,
					OutputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"summary": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}
	wfBytes, err := json.Marshal(wf)
	require.NoError(t, err)
	var wfSnapshot map[string]any
	require.NoError(t, json.Unmarshal(wfBytes, &wfSnapshot))

	// Create an instance in waiting state on the interactive node.
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:               instanceID,
		State:            instance.StateWaiting,
		CurrentNodeID:    &nodeID,
		WorkflowSnapshot: wfSnapshot,
		Variables:        map[string]any{},
	}
	instRepo.mu.Unlock()

	// Create an initial node execution record with conversation history.
	initialConversation := map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "Initial orchestration prompt"},
			{"role": "assistant", "content": "Hello! How can I help you?"},
		},
	}
	nodeExec := &instance.NodeExecution{
		ID:              uuid.New(),
		InstanceID:      instanceID,
		NodeID:          nodeID,
		Attempt:         1,
		Status:          instance.NodeStatusSucceeded,
		LLMConversation: initialConversation,
	}
	err = nodeExecRepo.Create(context.Background(), nodeExec)
	require.NoError(t, err)

	return instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID
}

func buildConversationHandler(
	instRepo *mockInstanceRepo,
	nodeExecRepo *mockNodeExecutionRepo,
	llmProvider *mockStreamingLLMProvider,
	eventBus *mockEventBus,
	queueMgr *mockQueueManager,
) engine.ConversationHandler {
	return engine.NewConversationHandler(
		instRepo,
		nodeExecRepo,
		llmProvider,
		eventBus,
		queueMgr,
	)
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: Happy path (normal response, no tool call)
// ---------------------------------------------------------------------------

func TestConversationHandler_HandleMessage_AppendsUserMessage(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"Sure", ", I can help."}, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "What is the weather?")
	require.NoError(t, err)

	// Verify user message was appended to the conversation.
	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)

	conv := nodeExec.LLMConversation
	messages := extractMessages(t, conv)

	// Should have: initial user, initial assistant, new user, new assistant = 4 messages
	require.Len(t, messages, 4)
	assert.Equal(t, "user", messages[2]["role"])
	assert.Equal(t, "What is the weather?", messages[2]["content"])
}

func TestConversationHandler_HandleMessage_PublishesUserMessageEvent(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"Response"}, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Hello")
	require.NoError(t, err)

	// Verify interactive.message event was published for the user message.
	msgEvents := eventBus.eventsOfType(instanceID, "interactive.message")
	require.GreaterOrEqual(t, len(msgEvents), 1)

	// The first interactive.message event should be for the user.
	data, ok := msgEvents[0].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", data["role"])
	assert.Equal(t, "Hello", data["content"])
}

func TestConversationHandler_HandleMessage_PublishesTokenEvents(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " ", "world"}, 5, 15)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Hi")
	require.NoError(t, err)

	tokenEvents := eventBus.eventsOfType(instanceID, "interactive.token")
	require.Len(t, tokenEvents, 3)

	tokens := make([]string, 0, len(tokenEvents))
	for _, e := range tokenEvents {
		data, ok := e.Data.(map[string]any)
		require.True(t, ok)
		tokens = append(tokens, data["token"].(string))
	}
	assert.Equal(t, []string{"Hello", " ", "world"}, tokens)
}

func TestConversationHandler_HandleMessage_AppendsAssistantResponse(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"Sure", ", I can help."}, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Help me")
	require.NoError(t, err)

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)

	messages := extractMessages(t, nodeExec.LLMConversation)
	require.Len(t, messages, 4)
	assert.Equal(t, "assistant", messages[3]["role"])
	assert.Equal(t, "Sure, I can help.", messages[3]["content"])
}

func TestConversationHandler_HandleMessage_PublishesAssistantMessageEvent(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"Done"}, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Go")
	require.NoError(t, err)

	msgEvents := eventBus.eventsOfType(instanceID, "interactive.message")
	require.Len(t, msgEvents, 2) // user + assistant

	data, ok := msgEvents[1].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", data["role"])
	assert.Equal(t, "Done", data["content"])
}

func TestConversationHandler_HandleMessage_SendsFullConversationToLLM(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "New question")
	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	req := reqs[0]

	// Should have system prompt.
	assert.Equal(t, "You are a helpful assistant.", req.SystemPrompt)

	// Should have the full conversation history as Messages.
	// Initial user + initial assistant + new user = 3 messages.
	require.Len(t, req.Messages, 3)
	assert.Equal(t, "user", req.Messages[0].Role)
	assert.Equal(t, "Initial orchestration prompt", req.Messages[0].Content)
	assert.Equal(t, "assistant", req.Messages[1].Role)
	assert.Equal(t, "Hello! How can I help you?", req.Messages[1].Content)
	assert.Equal(t, "user", req.Messages[2].Role)
	assert.Equal(t, "New question", req.Messages[2].Content)

	// Should have the complete_session tool.
	require.Len(t, req.Tools, 1)
	assert.Equal(t, "complete_session", req.Tools[0].Name)

	// Should have model settings.
	assert.Equal(t, "claude-sonnet-4-20250514", req.Model)
	assert.Equal(t, 4096, req.MaxTokens)
	assert.Equal(t, 0.7, req.Temperature)
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: Session completion (tool call)
// ---------------------------------------------------------------------------

// cannedToolStream returns a StreamCall function that emits tokens then a final
// Done chunk with a ToolUse indicating the complete_session tool was called.
func cannedToolStream(tokens []string, toolInput map[string]any, tokensIn, tokensOut int) func(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	return func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		ch := make(chan engine.LLMStreamChunk, len(tokens)+1)
		for _, t := range tokens {
			ch <- engine.LLMStreamChunk{Token: t}
		}
		ch <- engine.LLMStreamChunk{
			Done:      true,
			TokensIn:  tokensIn,
			TokensOut: tokensOut,
			ToolUse: &engine.LLMToolUse{
				Name:  "complete_session",
				Input: toolInput,
			},
		}
		close(ch)
		return ch, nil
	}
}

func TestConversationHandler_HandleMessage_SessionCompletion_ExtractsOutput(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	toolInput := map[string]any{"summary": "Meeting went well"}
	llmProvider.streamCallFn = cannedToolStream([]string{"Let me complete"}, toolInput, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Please summarize")
	require.NoError(t, err)

	// Verify output was stored on node execution record.
	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	require.NotNil(t, nodeExec.Output)
	assert.Equal(t, "Meeting went well", nodeExec.Output["summary"])
}

func TestConversationHandler_HandleMessage_SessionCompletion_UpdatesInstanceVariables(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	toolInput := map[string]any{"summary": "All good"}
	llmProvider.streamCallFn = cannedToolStream([]string{""}, toolInput, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Wrap up")
	require.NoError(t, err)

	// Verify instance variables were updated with node output namespaced under node ID.
	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	nodeOutput, ok := inst.Variables["interact"]
	require.True(t, ok, "instance variables should contain node output under 'interact' key")
	outputMap, ok := nodeOutput.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "All good", outputMap["summary"])
}

func TestConversationHandler_HandleMessage_SessionCompletion_TransitionsToQueued(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	toolInput := map[string]any{"summary": "Done"}
	llmProvider.streamCallFn = cannedToolStream([]string{""}, toolInput, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Finish")
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestConversationHandler_HandleMessage_SessionCompletion_PublishesEndedEvent(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	toolInput := map[string]any{"summary": "Complete"}
	llmProvider.streamCallFn = cannedToolStream([]string{""}, toolInput, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "End it")
	require.NoError(t, err)

	endedEvents := eventBus.eventsOfType(instanceID, "interactive.ended")
	require.Len(t, endedEvents, 1)

	data, ok := endedEvents[0].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "llm_completed", data["reason"])
	assert.NotNil(t, data["output"])
}

func TestConversationHandler_HandleMessage_SessionCompletion_NotifiesCapacityFreed(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	toolInput := map[string]any{"summary": "Done"}
	llmProvider.streamCallFn = cannedToolStream([]string{""}, toolInput, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Finish")
	require.NoError(t, err)

	// Verify NotifyCapacityFreed was called.
	select {
	case <-queueMgr.notifyCapacityFreedCh:
		// success
	default:
		t.Fatal("expected NotifyCapacityFreed to be called")
	}
}

func TestConversationHandler_HandleMessage_SessionCompletion_PersistsConversation(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	toolInput := map[string]any{"summary": "Complete"}
	llmProvider.streamCallFn = cannedToolStream([]string{"Completing"}, toolInput, 10, 20)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Summarize")
	require.NoError(t, err)

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)

	messages := extractMessages(t, nodeExec.LLMConversation)
	// Should have: initial user, initial assistant, new user, assistant tool response = 4
	require.Len(t, messages, 4)
	// The assistant message should be present (content from tokens).
	assert.Equal(t, "assistant", messages[3]["role"])
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: Invalid state
// ---------------------------------------------------------------------------

func TestConversationHandler_HandleMessage_NotWaiting_ReturnsError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Set instance to running instead of waiting.
	instRepo.mu.Lock()
	instRepo.instances[instanceID].State = instance.StateRunning
	instRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in interactive wait state")
}

func TestConversationHandler_HandleMessage_NotInteractiveNode_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingLLMProvider()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	instanceID := uuid.New()
	nodeID := "script-node"

	// Build workflow with non-interactive node.
	wf := workflow.Workflow{
		Name: "test-wf",
		Nodes: []workflow.Node{
			{
				ID:         nodeID,
				Type:       workflow.NodeTypeScript,
				Entrypoint: true,
			},
		},
	}
	wfBytes, _ := json.Marshal(wf)
	var wfSnapshot map[string]any
	_ = json.Unmarshal(wfBytes, &wfSnapshot)

	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:               instanceID,
		State:            instance.StateWaiting,
		CurrentNodeID:    &nodeID,
		WorkflowSnapshot: wfSnapshot,
		Variables:        map[string]any{},
	}
	instRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not interactive")
}

func TestConversationHandler_HandleMessage_InstanceNotFound_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingLLMProvider()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), uuid.New(), "Hello")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// EndSession Tests: With existing output
// ---------------------------------------------------------------------------

func TestConversationHandler_EndSession_WithExistingOutput_PreservesOutput(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Set existing output on node execution.
	nodeExecRepo.mu.Lock()
	if len(nodeExecRepo.executions) > 0 {
		nodeExecRepo.executions[0].Output = map[string]any{"summary": "Prior output"}
	}
	nodeExecRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.NoError(t, err)

	// Verify the existing output was preserved.
	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	assert.Equal(t, "Prior output", nodeExec.Output["summary"])
}

func TestConversationHandler_EndSession_WithExistingOutput_TransitionsToQueued(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	nodeExecRepo.mu.Lock()
	if len(nodeExecRepo.executions) > 0 {
		nodeExecRepo.executions[0].Output = map[string]any{"summary": "Done"}
	}
	nodeExecRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestConversationHandler_EndSession_WithExistingOutput_UpdatesVariables(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	nodeExecRepo.mu.Lock()
	if len(nodeExecRepo.executions) > 0 {
		nodeExecRepo.executions[0].Output = map[string]any{"summary": "Prior output"}
	}
	nodeExecRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	nodeOutput, ok := inst.Variables["interact"]
	require.True(t, ok)
	outputMap, ok := nodeOutput.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Prior output", outputMap["summary"])
}

// ---------------------------------------------------------------------------
// EndSession Tests: Without existing output
// ---------------------------------------------------------------------------

func TestConversationHandler_EndSession_WithoutOutput_StoresEmptyObject(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.NoError(t, err)

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	require.NotNil(t, nodeExec.Output)
	assert.Empty(t, nodeExec.Output) // empty map
}

func TestConversationHandler_EndSession_WithoutOutput_TransitionsToQueued(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestConversationHandler_EndSession_PublishesEndedEvent(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.NoError(t, err)

	endedEvents := eventBus.eventsOfType(instanceID, "interactive.ended")
	require.Len(t, endedEvents, 1)

	data, ok := endedEvents[0].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user_ended", data["reason"])
}

func TestConversationHandler_EndSession_NotifiesCapacityFreed(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.NoError(t, err)

	select {
	case <-queueMgr.notifyCapacityFreedCh:
		// success
	default:
		t.Fatal("expected NotifyCapacityFreed to be called")
	}
}

// ---------------------------------------------------------------------------
// EndSession Tests: Invalid state
// ---------------------------------------------------------------------------

func TestConversationHandler_EndSession_NotWaiting_ReturnsError(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	instRepo.mu.Lock()
	instRepo.instances[instanceID].State = instance.StateRunning
	instRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), instanceID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in interactive wait state")
}

func TestConversationHandler_EndSession_InstanceNotFound_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	err := handler.EndSession(context.Background(), uuid.New())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: LLM error during streaming
// ---------------------------------------------------------------------------

func TestConversationHandler_HandleMessage_StreamError_ReturnsError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		return nil, fmt.Errorf("LLM unavailable")
	}

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM unavailable")
}

func TestConversationHandler_HandleMessage_StreamChunkError_ReturnsError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		ch := make(chan engine.LLMStreamChunk, 2)
		ch <- engine.LLMStreamChunk{Token: "partial"}
		ch <- engine.LLMStreamChunk{Error: fmt.Errorf("connection lost")}
		close(ch)
		return ch, nil
	}

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(context.Background(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractMessages pulls the messages array from an LLMConversation map.
func extractMessages(t *testing.T, conv map[string]any) []map[string]any {
	t.Helper()
	require.NotNil(t, conv)

	rawMessages, ok := conv["messages"]
	require.True(t, ok, "conversation should have 'messages' key")

	// The messages could be []map[string]any or []any depending on how they were stored.
	switch msgs := rawMessages.(type) {
	case []map[string]any:
		return msgs
	case []any:
		result := make([]map[string]any, len(msgs))
		for i, m := range msgs {
			msgMap, ok := m.(map[string]any)
			require.True(t, ok, "message %d should be a map", i)
			result[i] = msgMap
		}
		return result
	default:
		t.Fatalf("unexpected messages type: %T", rawMessages)
		return nil
	}
}
