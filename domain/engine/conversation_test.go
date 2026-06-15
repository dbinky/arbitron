package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers for conversation handler tests
// ---------------------------------------------------------------------------

// convoTestOwner owns the interactive instances created by the conversation
// test setups. convoCtx carries that owner identity so the ownership gate in
// loadAndValidate (Phase 4 Task 4) admits the participate calls.
var convoTestOwner = uuid.New()

func convoCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: convoTestOwner, Username: "owner", Role: identity.RoleUser})
}

// conversationTestSetup creates the standard mock dependencies and an instance
// in the "waiting" state on an interactive node, with an existing node execution
// record containing the initial conversation.
func conversationTestSetup(t *testing.T) (
	*mockInstanceRepo,
	*mockNodeExecutionRepo,
	*mockStreamingChatProvider,
	*mockEventBus,
	*mockQueueManager,
	uuid.UUID,
) {
	t.Helper()

	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingChatProvider()
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
				Type:       workflow.NodeTypeInteractiveLLM,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					SystemPrompt: "You are a helpful assistant.",
					ModelProfile: interactiveChatProfile,
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
		OwnerID:          convoTestOwner,
		State:            instance.StateWaiting,
		CurrentNodeID:    &nodeID,
		WorkflowSnapshot: wfSnapshot,
		Variables:        map[string]any{},
	}
	instRepo.mu.Unlock()

	// Create an initial node execution record with conversation history.
	// The nonce mirrors what InteractiveExecutor would have persisted on the
	// first turn; ConversationHandler reads it to scope sentinel parsing.
	initialConversation := map[string]any{
		"nonce": testNonce,
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
	llmProvider *mockStreamingChatProvider,
	eventBus *mockEventBus,
	queueMgr *mockQueueManager,
) engine.ConversationHandler {
	return engine.NewConversationHandler(
		instRepo,
		nodeExecRepo,
		streamSelector(llmProvider),
		interactiveResolver(),
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

	err := handler.HandleMessage(convoCtx(), instanceID, "What is the weather?")
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

	err := handler.HandleMessage(convoCtx(), instanceID, "Hello")
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

	err := handler.HandleMessage(convoCtx(), instanceID, "Hi")
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

	err := handler.HandleMessage(convoCtx(), instanceID, "Help me")
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

	err := handler.HandleMessage(convoCtx(), instanceID, "Go")
	require.NoError(t, err)

	msgEvents := eventBus.eventsOfType(instanceID, "interactive.message")
	require.Len(t, msgEvents, 2) // user + assistant

	data, ok := msgEvents[1].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", data["role"])
	assert.Equal(t, "Done", data["content"])
}

func TestConversationHandler_HandleMessage_ResumesStoredSession(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "New question")
	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	req := reqs[0]

	assert.Equal(t, "You are a helpful assistant.", req.SystemPrompt)
	// SF8 / PR #8 thread 3291907744: Messages and Tools were removed from
	// ChatRequest. Resumed turns use Prompt; the dead invariants are no
	// longer expressible.
	assert.Equal(t, "New question", req.Prompt)
	assert.Equal(t, "claude-sonnet-4-20250514", req.Model)
}

func TestConversationHandler_HandleMessage_AgentProfile_UsesAgentStreamAndBindingModel(t *testing.T) {
	// A resumed turn on an interactive_agent node must resolve the node's
	// model_profile to an AGENT binding, select the agent (tool-enabled) stream
	// provider, and send binding.Model on the request.
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	instanceID := uuid.New()
	nodeID := "agent-interact"

	wf := workflow.Workflow{
		Name: "test-wf",
		Nodes: []workflow.Node{
			{
				ID:         nodeID,
				Type:       workflow.NodeTypeInteractiveAgent,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					SystemPrompt: "You are an agent.",
					ModelProfile: "agent-prof",
				},
			},
		},
	}
	wfBytes, err := json.Marshal(wf)
	require.NoError(t, err)
	var wfSnapshot map[string]any
	require.NoError(t, json.Unmarshal(wfBytes, &wfSnapshot))

	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:               instanceID,
		OwnerID:          convoTestOwner,
		State:            instance.StateWaiting,
		CurrentNodeID:    &nodeID,
		WorkflowSnapshot: wfSnapshot,
		Variables:        map[string]any{},
	}
	instRepo.mu.Unlock()

	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     nodeID,
		Attempt:    1,
		Status:     instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{
			"nonce": testNonce,
			"messages": []map[string]any{
				{"role": "user", "content": "Initial"},
				{"role": "assistant", "content": "Ready."},
			},
		},
	}))

	chatProv := newMockStreamingChatProvider()
	agentProv := newMockStreamingChatProvider()
	agentProv.streamCallFn = cannedStream([]string{"agent turn"}, 3, 4)

	resolver := newFakeResolver(provider.Profile{
		Alias: "agent-prof",
		Kind:  provider.ProfileKindAgent,
		Model: "claude-opus-agent",
	})
	sel := &fakeStreamSelector{chatProv: chatProv, agentProv: agentProv}

	handler := engine.NewConversationHandler(instRepo, nodeExecRepo, sel, resolver, eventBus, queueMgr)

	require.NoError(t, handler.HandleMessage(convoCtx(), instanceID, "Do the thing"))

	assert.Equal(t, provider.ProfileKindAgent, sel.gotKind)
	assert.Empty(t, chatProv.getCapturedReqs(), "chat provider must not be used for an agent profile")
	agentReqs := agentProv.getCapturedReqs()
	require.Len(t, agentReqs, 1)
	assert.Equal(t, "claude-opus-agent", agentReqs[0].Model, "request model must come from binding.Model")
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: Session completion (tool call)
// ---------------------------------------------------------------------------

// testNonce is the per-instance sentinel nonce used across conversation
// handler tests. Must match the value seeded into LLMConversation by
// conversationTestSetup so the handler accepts the sentinel block.
const testNonce = "test-nonce-fixed"

// cannedSentinelStream emits a response whose text contains a completion
// sentinel block carrying result, plus a Done chunk with a session id. The
// sentinel uses testNonce so the conversation handler (which reads the nonce
// from LLMConversation["nonce"]) accepts it.
func cannedSentinelStream(prefix string, result map[string]any, sessionID string) func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		payload, _ := json.Marshal(result)
		body := prefix + engine.SentinelOpenTag(testNonce) + string(payload) + engine.SentinelCloseTag(testNonce)
		ch := make(chan engine.ChatChunk, 2)
		ch <- engine.ChatChunk{Token: body}
		ch <- engine.ChatChunk{Done: true, SessionID: sessionID}
		close(ch)
		return ch, nil
	}
}

func TestConversationHandler_HandleMessage_SessionCompletion_ExtractsOutput(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	result := map[string]any{"summary": "Meeting went well"}
	llmProvider.streamCallFn = cannedSentinelStream("Let me complete\n", result, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Please summarize")
	require.NoError(t, err)

	// Verify output was stored on node execution record.
	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	require.NotNil(t, nodeExec.Output)
	assert.Equal(t, "Meeting went well", nodeExec.Output["summary"])
}

func TestConversationHandler_HandleMessage_SessionCompletion_UpdatesInstanceVariables(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	result := map[string]any{"summary": "All good"}
	llmProvider.streamCallFn = cannedSentinelStream("\n", result, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Wrap up")
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

	result := map[string]any{"summary": "Done"}
	llmProvider.streamCallFn = cannedSentinelStream("\n", result, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Finish")
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestConversationHandler_HandleMessage_SessionCompletion_PublishesEndedEvent(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	result := map[string]any{"summary": "Complete"}
	llmProvider.streamCallFn = cannedSentinelStream("\n", result, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "End it")
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

	result := map[string]any{"summary": "Done"}
	llmProvider.streamCallFn = cannedSentinelStream("\n", result, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Finish")
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

	result := map[string]any{"summary": "Complete"}
	llmProvider.streamCallFn = cannedSentinelStream("Completing\n", result, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Summarize")
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

	err := handler.HandleMessage(convoCtx(), instanceID, "Hello")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
}

func TestConversationHandler_HandleMessage_NotInteractiveNode_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingChatProvider()
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
		OwnerID:          convoTestOwner,
		State:            instance.StateWaiting,
		CurrentNodeID:    &nodeID,
		WorkflowSnapshot: wfSnapshot,
		Variables:        map[string]any{},
	}
	instRepo.mu.Unlock()

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not interactive")
}

func TestConversationHandler_HandleMessage_InstanceNotFound_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingChatProvider()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), uuid.New(), "Hello")
	require.Error(t, err)
}

func TestConversationHandler_HandleMessage_NodeExecNotFound_ReturnsError(t *testing.T) {
	// The instance is in waiting state on an interactive node, but there is no
	// corresponding node execution record. HandleMessage must return an error
	// rather than panicking or silently proceeding.
	instRepo, _, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	emptyNodeExecRepo := newMockNodeExecutionRepo() // no executions registered

	handler := buildConversationHandler(instRepo, emptyNodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load node execution")
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

	_, err := handler.EndSession(convoCtx(), instanceID)
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

	_, err := handler.EndSession(convoCtx(), instanceID)
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

	_, err := handler.EndSession(convoCtx(), instanceID)
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

	_, err := handler.EndSession(convoCtx(), instanceID)
	require.NoError(t, err)

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	require.NotNil(t, nodeExec.Output)
	assert.Empty(t, nodeExec.Output) // empty map
}

func TestConversationHandler_EndSession_WithoutOutput_TransitionsToQueued(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	_, err := handler.EndSession(convoCtx(), instanceID)
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestConversationHandler_EndSession_PublishesEndedEvent(t *testing.T) {
	instRepo, nodeExecRepo, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	_, err := handler.EndSession(convoCtx(), instanceID)
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

	_, err := handler.EndSession(convoCtx(), instanceID)
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

	_, err := handler.EndSession(convoCtx(), instanceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
}

func TestConversationHandler_EndSession_InstanceNotFound_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	handler := buildConversationHandler(instRepo, nodeExecRepo, nil, eventBus, queueMgr)

	_, err := handler.EndSession(convoCtx(), uuid.New())
	require.Error(t, err)
}

func TestConversationHandler_EndSession_NodeExecNotFound_ReturnsError(t *testing.T) {
	// The instance exists and is waiting, but there is no node execution record.
	// EndSession must return an error rather than panicking.
	instRepo, _, _, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	emptyNodeExecRepo := newMockNodeExecutionRepo() // no executions registered

	handler := buildConversationHandler(instRepo, emptyNodeExecRepo, nil, eventBus, queueMgr)

	_, err := handler.EndSession(convoCtx(), instanceID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load node execution")
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: Edge cases
// ---------------------------------------------------------------------------

func TestConversationHandler_HandleMessage_NilConversationHistory_StartsFirstMessage(t *testing.T) {
	// Edge: the node execution record has no prior conversation (nil
	// LLMConversation). parseConversation returns nil, and append(nil, msg)
	// is safe in Go — the user message becomes the first and only message
	// sent to the LLM. This scenario occurs after crash recovery if the
	// instance was set to waiting before the executor wrote its record.
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Clear the conversation on the existing nodeExec.
	nodeExecRepo.mu.Lock()
	if len(nodeExecRepo.executions) > 0 {
		nodeExecRepo.executions[0].LLMConversation = nil
	}
	nodeExecRepo.mu.Unlock()

	llmProvider.streamCallFn = cannedStream([]string{"Sure"}, 5, 10)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Hello from scratch")
	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	// SF8: Messages field removed; assertion no longer expressible.
	assert.Equal(t, "Hello from scratch", reqs[0].Prompt)
	assert.Empty(t, reqs[0].SessionID, "no stored session id yet")

	// Conversation should now have user + assistant messages.
	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	messages := extractMessages(t, nodeExec.LLMConversation)
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0]["role"])
	assert.Equal(t, "Hello from scratch", messages[0]["content"])
	assert.Equal(t, "assistant", messages[1]["role"])
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: Session resumption and sentinel completion
// ---------------------------------------------------------------------------

func TestConversationHandler_HandleMessage_ResumesViaStoredSessionID(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Seed a stored claude session id on the existing node execution.
	nodeExecRepo.mu.Lock()
	nodeExecRepo.executions[0].LLMConversation["sessionId"] = "s-prev"
	nodeExecRepo.mu.Unlock()

	llmProvider.streamCallFn = cannedStream([]string{"resumed reply"}, 5, 10)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	require.NoError(t, handler.HandleMessage(convoCtx(), instanceID, "next message"))

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, "s-prev", reqs[0].SessionID)
	assert.Equal(t, "next message", reqs[0].Prompt)
	// SF8: Messages field removed.

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	assert.Equal(t, "s-prev", nodeExec.LLMConversation["sessionId"],
		"stored session id must be preserved when the stream carries none")
}

func TestConversationHandler_HandleMessage_SentinelCompletesSession(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedSentinelStream("All set.\n",
		map[string]any{"picked": []any{"a", "b"}}, "s-final")

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	require.NoError(t, handler.HandleMessage(convoCtx(), instanceID, "choose"))

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, nodeExec.Output["picked"])
	assert.Equal(t, "s-final", nodeExec.LLMConversation["sessionId"],
		"session id from the stream must be persisted")

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestConversationHandler_HandleMessage_NoSentinelIsNormalTurn(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"just chatting"}, 5, 10)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	require.NoError(t, handler.HandleMessage(convoCtx(), instanceID, "hi"))

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State, "no sentinel: session stays open")

	nodeExec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	messages := extractMessages(t, nodeExec.LLMConversation)
	assert.Equal(t, "assistant", messages[len(messages)-1]["role"])
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: LLM error during streaming
// ---------------------------------------------------------------------------

func TestConversationHandler_HandleMessage_StreamError_ReturnsError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		return nil, fmt.Errorf("LLM unavailable")
	}

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM unavailable")
}

func TestConversationHandler_HandleMessage_StreamChunkError_ReturnsError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, 2)
		ch <- engine.ChatChunk{Token: "partial"}
		ch <- engine.ChatChunk{Error: fmt.Errorf("connection lost")}
		close(ch)
		return ch, nil
	}

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err := handler.HandleMessage(convoCtx(), instanceID, "Hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

// ---------------------------------------------------------------------------
// HandleMessage Tests: Engine seam (PAIRED FA#19)
// ---------------------------------------------------------------------------

// PAIRED (FA#19) — WorkDir flows from the instance's workspace variable to the
// ChatRequest so the InteractiveProvider can use it as its sandbox root guard.
// An empty workspace would cause InteractiveProvider to fail closed with
// ErrWorkDirRequired; this test proves the conversation handler correctly
// propagates the instance workspace to the request, using the existing
// StreamProviderFor path with zero engine edits.
func TestConversationHandler_HandleMessage_WorkDirFlowsFromWorkspace(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	// Seed the workspace variable on the instance.
	instRepo.mu.Lock()
	instRepo.instances[instanceID].Variables["workspace"] = "/tmp/conv-ws"
	instRepo.mu.Unlock()

	llmProvider.streamCallFn = cannedStream([]string{"ok"}, 3, 4)

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	require.NoError(t, handler.HandleMessage(convoCtx(), instanceID, "help"))

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/tmp/conv-ws", reqs[0].WorkDir,
		"WorkDir must flow from inst.Variables[workspace] so the InteractiveProvider can use it as its sandbox root guard")
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

// ---------------------------------------------------------------------------
// HandleMessage Tests: concurrency safety (one in-flight turn per session)
// ---------------------------------------------------------------------------

// A second message sent while a turn is already in flight for the SAME instance
// must be rejected before it reaches the provider. Without this guard, two
// overlapping sends spawn concurrent `claude --resume` on the same on-disk
// session, which claude-cli cannot handle: it queues the prompt and the session
// no-ops ("No response requested."), permanently wedging the interactive node.
func TestConversationHandler_HandleMessage_RejectsConcurrentTurnSameInstance(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var providerCalls int32
	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		atomic.AddInt32(&providerCalls, 1)
		entered <- struct{}{}
		<-release // hold the turn in flight, simulating a slow claude --resume
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Done: true}
		close(ch)
		return ch, nil
	}

	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	// First send enters the provider and blocks: a turn is now in flight.
	firstErr := make(chan error, 1)
	go func() { firstErr <- handler.HandleMessage(convoCtx(), instanceID, "first") }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("first send never reached the provider")
	}

	// Second concurrent send for the same instance: must be rejected before the
	// provider is ever called a second time.
	secondErr := make(chan error, 1)
	go func() { secondErr <- handler.HandleMessage(convoCtx(), instanceID, "second") }()

	select {
	case err := <-secondErr:
		require.Error(t, err, "second concurrent send must be rejected while a turn is in flight")
		assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress)
	case <-entered:
		close(release)
		t.Fatal("second concurrent send reached the provider — concurrent claude --resume would corrupt the session")
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("second concurrent send neither returned nor was rejected (deadlock)")
	}

	// Releasing the first turn lets it finish cleanly; the provider was entered once.
	close(release)
	require.NoError(t, <-firstErr)
	assert.Equal(t, int32(1), atomic.LoadInt32(&providerCalls), "provider must be entered exactly once")
}

// The in-flight guard must be released when a turn completes, so a normal
// sequence of turns on the same instance is unaffected (only *overlapping*
// sends are rejected). Guards against a never-released guard regression.
func TestConversationHandler_HandleMessage_SequentialTurnsAllowedAfterRelease(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	llmProvider.streamCallFn = cannedStream([]string{"ok"}, 1, 1)
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	require.NoError(t, handler.HandleMessage(convoCtx(), instanceID, "first"))
	// The first turn has returned, so its guard is released: the second send
	// must NOT be rejected as a concurrent turn.
	err := handler.HandleMessage(convoCtx(), instanceID, "second")
	require.NoError(t, err)
	assert.NotErrorIs(t, err, instance.ErrInteractiveTurnInProgress)
}

// Security: the auth/state gate in loadAndValidate must run BEFORE the in-flight
// guard. Otherwise a caller who cannot even view the instance, sending during an
// in-flight turn, would receive ErrInteractiveTurnInProgress — leaking that a
// turn is active (a cross-boundary existence/activity oracle) and letting an
// unauthorized caller perturb the owner's guard. The unauthorized caller must
// get ErrNotFound and learn nothing about the session's state.
func TestConversationHandler_HandleMessage_AuthRunsBeforeInFlightGuard(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		entered <- struct{}{}
		<-release
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Done: true}
		close(ch)
		return ch, nil
	}
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	// Owner holds a turn in flight.
	ownerErr := make(chan error, 1)
	go func() { ownerErr <- handler.HandleMessage(convoCtx(), instanceID, "owner turn") }()
	<-entered

	// A caller who cannot view the instance sends concurrently.
	intruderCtx := identity.WithIdentity(context.Background(),
		identity.Identity{UserID: uuid.New(), Username: "intruder", Role: identity.RoleUser})
	err := handler.HandleMessage(intruderCtx, instanceID, "intruder")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound, "auth must run before the in-flight guard")
	assert.NotErrorIs(t, err, instance.ErrInteractiveTurnInProgress,
		"must not leak in-flight turn state to an unauthorized caller")

	close(release)
	require.NoError(t, <-ownerErr)
}

// EndSession must not race a turn that is still in flight: ending mid-turn would
// transition the instance to queued while HandleMessage is still streaming and
// about to persist, a double-completion race. EndSession acquires the same
// per-instance guard and is rejected while a turn is in progress.
func TestConversationHandler_EndSession_RejectedDuringInFlightTurn(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		entered <- struct{}{}
		<-release
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Done: true}
		close(ch)
		return ch, nil
	}
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	turnErr := make(chan error, 1)
	go func() { turnErr <- handler.HandleMessage(convoCtx(), instanceID, "turn") }()
	<-entered

	_, err := handler.EndSession(convoCtx(), instanceID)
	require.Error(t, err, "EndSession must be rejected while a turn is in flight")
	assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress)

	close(release)
	require.NoError(t, <-turnErr)
}

// Regression guard: the in-flight guard keys on instanceID, NOT a global lock —
// two DIFFERENT instances must run turns concurrently. A future switch to a
// single shared mutex (or a wrong key) would serialize all interactive sessions
// and this would deadlock the second turn.
func TestConversationHandler_HandleMessage_DifferentInstancesRunConcurrently(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instA := conversationTestSetup(t)

	// Add a second waiting interactive instance sharing the same repos + handler.
	instB := uuid.New()
	nodeID := "interact"
	instRepo.mu.Lock()
	snap := instRepo.instances[instA].WorkflowSnapshot
	instRepo.instances[instB] = &instance.Instance{
		ID: instB, OwnerID: convoTestOwner, State: instance.StateWaiting,
		CurrentNodeID: &nodeID, WorkflowSnapshot: snap, Variables: map[string]any{},
	}
	instRepo.mu.Unlock()
	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instB, NodeID: nodeID, Attempt: 1,
		Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{"nonce": testNonce, "messages": []map[string]any{
			{"role": "user", "content": "init"}, {"role": "assistant", "content": "hi"}}},
	}))

	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		entered <- struct{}{}
		<-release
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Done: true}
		close(ch)
		return ch, nil
	}
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	errA := make(chan error, 1)
	errB := make(chan error, 1)
	go func() { errA <- handler.HandleMessage(convoCtx(), instA, "a") }()
	go func() { errB <- handler.HandleMessage(convoCtx(), instB, "b") }()

	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("both different instances must reach the provider concurrently (per-instance keying)")
		}
	}
	close(release)
	require.NoError(t, <-errA)
	require.NoError(t, <-errB)
}

// Regression guard: the guard is released even when a turn ERRORS (the defer must
// not drift onto the success path). A failed turn that left the guard held would
// permanently reject all later sends — re-creating the original wedge.
func TestConversationHandler_HandleMessage_GuardReleasedAfterError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)

	var calls int
	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("provider boom") // first turn fails
		}
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Done: true}
		close(ch)
		return ch, nil
	}
	handler := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)

	err1 := handler.HandleMessage(convoCtx(), instanceID, "first")
	require.Error(t, err1)
	assert.NotErrorIs(t, err1, instance.ErrInteractiveTurnInProgress)

	// The guard must have been released despite the error, so the next send runs.
	err2 := handler.HandleMessage(convoCtx(), instanceID, "second")
	require.NoError(t, err2)
	assert.NotErrorIs(t, err2, instance.ErrInteractiveTurnInProgress)
}
