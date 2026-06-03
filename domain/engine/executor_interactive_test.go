package engine_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock: streaming LLM provider for interactive executor tests
// ---------------------------------------------------------------------------

type mockStreamingChatProvider struct {
	mu            sync.Mutex
	streamCallFn  func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)
	capturedReqs  []engine.ChatRequest
}

func newMockStreamingChatProvider() *mockStreamingChatProvider {
	return &mockStreamingChatProvider{}
}

func (m *mockStreamingChatProvider) Chat(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
	return nil, fmt.Errorf("Chat not implemented for streaming mock")
}

func (m *mockStreamingChatProvider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	m.mu.Lock()
	m.capturedReqs = append(m.capturedReqs, req)
	fn := m.streamCallFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return nil, fmt.Errorf("no stream call function configured")
}

func (m *mockStreamingChatProvider) getCapturedReqs() []engine.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]engine.ChatRequest, len(m.capturedReqs))
	copy(cp, m.capturedReqs)
	return cp
}

// cannedStream returns a StreamChat function that emits the given tokens
// and a final Done chunk with token counts.
func cannedStream(tokens []string, tokensIn, tokensOut int) func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, len(tokens)+1)
		for _, t := range tokens {
			ch <- engine.ChatChunk{Token: t}
		}
		ch <- engine.ChatChunk{Done: true, TokensIn: tokensIn, TokensOut: tokensOut}
		close(ch)
		return ch, nil
	}
}

// cannedStreamWithSession is cannedStream plus a session id on the Done chunk.
func cannedStreamWithSession(tokens []string, sessionID string) func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, len(tokens)+1)
		for _, tok := range tokens {
			ch <- engine.ChatChunk{Token: tok}
		}
		ch <- engine.ChatChunk{Done: true, SessionID: sessionID}
		close(ch)
		return ch, nil
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func interactiveTestSetup() (
	*mockInstanceRepo,
	*mockNodeExecutionRepo,
	*mockStreamingChatProvider,
	*mockFileReader,
	*mockEventBus,
	*mockQueueManager,
	uuid.UUID,
) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingChatProvider()
	fileReader := newMockFileReader()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	return instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID
}

// interactiveChatProfile is the chat model_profile alias used by the standard
// interactiveNode. The fakeResolver below resolves it to a chat binding whose
// Model is the sonnet model the existing assertions expect.
const interactiveChatProfile = "interactive-chat"
const interactiveChatModel = "claude-sonnet-4-20250514"

// interactiveResolver returns a fakeResolver that knows the chat profile used by
// interactiveNode (resolving to a chat binding with interactiveChatModel).
func interactiveResolver() *fakeResolver {
	return newFakeResolver(provider.Profile{
		Alias: interactiveChatProfile,
		Kind:  provider.ProfileKindChat,
		Model: interactiveChatModel,
	})
}

func interactiveNode() workflow.Node {
	return workflow.Node{
		ID:   "interact",
		Type: workflow.NodeTypeInteractiveLLM,
		Config: workflow.NodeConfig{
			SystemPrompt:      "You are a helpful assistant.",
			OrchestrationFile: "orchestration.md",
			TemplateFile:      "template.md",
			ModelProfile:      interactiveChatProfile,
			MaxTokens:         4096,
			Temperature:       0.7,
			OutputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string"},
				},
			},
		},
	}
}

func buildInteractiveExecutor(
	instRepo *mockInstanceRepo,
	nodeExecRepo *mockNodeExecutionRepo,
	llmProvider *mockStreamingChatProvider,
	fileReader *mockFileReader,
	eventBus *mockEventBus,
	queueMgr *mockQueueManager,
) *engine.InteractiveExecutor {
	return engine.NewInteractiveExecutor(
		instRepo,
		nodeExecRepo,
		streamSelector(llmProvider),
		interactiveResolver(),
		fileReader,
		eventBus,
		queueMgr,
	)
}

// ---------------------------------------------------------------------------
// Tests: Happy path
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_ReadsOrchestrationFile(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration content here")
	fileReader.files["test-wf/template.md"] = []byte("Template content")

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " there"}, 10, 20)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify file reading was called (by checking that the LLM received the content)
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0].Prompt, "Orchestration content here")
}

func TestInteractiveExecutor_ReadsTemplateFile(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template content")

	llmProvider.streamCallFn = cannedStream([]string{"Response"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0].Prompt, "Template content")
}

func TestInteractiveExecutor_VariableSubstitutionInTemplate(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Hello {{name}}, please review {{topic}}")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	vars := variable.NewStore()
	vars.SetInput("name", "Alice")
	vars.SetInput("topic", "Q3 report")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), vars, "test-wf")

	require.NoError(t, err)
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0].Prompt, "Hello Alice, please review Q3 report")
}

func TestInteractiveExecutor_StreamChatInvokedWithCorrectRequest(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration instructions")
	fileReader.files["test-wf/template.md"] = []byte("Template data")

	llmProvider.streamCallFn = cannedStream([]string{"response"}, 15, 25)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	node := interactiveNode()
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")

	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	req := reqs[0]

	// Verify system prompt
	assert.Equal(t, "You are a helpful assistant.", req.SystemPrompt)

	// Verify user message contains orchestration + template
	assert.Contains(t, req.Prompt, "Orchestration instructions")
	assert.Contains(t, req.Prompt, "Template data")

	// Verify model
	assert.Equal(t, "claude-sonnet-4-20250514", req.Model)

	// Verify max tokens
	assert.Equal(t, 4096, req.MaxTokens)

	// Verify temperature
	assert.Equal(t, 0.7, req.Temperature)

	// SF8 / PR #8 thread 3291907744: Tools and Messages were removed from
	// ChatRequest. The initial interactive call uses Prompt; the dead
	// invariants are no longer expressible.
}

func TestInteractiveExecutor_PublishesTokenEvents(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " ", "world"}, 5, 15)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	// Verify interactive.token events were published
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

func TestInteractiveExecutor_PublishesFinalAssistantMessageEvent(t *testing.T) {
	// After streaming the first response, the executor must publish a
	// terminating interactive.message (role=assistant) so a live client that
	// watched the token stream resets its isStreaming flag. The resumed-turn
	// ConversationHandler already does this; the initial executor must match,
	// otherwise the dashboard composer stays disabled until a page reload
	// triggers interactive.history.
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " world"}, 5, 10)

	// Snapshot the number of events published at the moment the instance is
	// transitioned to waiting (step 11). The finalizing interactive.message
	// must be emitted at-or-after this point so a live client is only told the
	// turn completed once the instance is durable and accepting input.
	//
	// Scope: asserts message emits after UpdateState(waiting); does not pin
	// message-vs-Create — see swallowed-Create follow-up.
	waitingSnapshot := -1
	instRepo.onUpdateState = func(_ uuid.UUID, state instance.State) {
		if state == instance.StateWaiting {
			waitingSnapshot = eventBus.eventCount(instanceID)
		}
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	msgEvents := eventBus.eventsOfType(instanceID, "interactive.message")
	require.Len(t, msgEvents, 1, "executor must publish exactly one finalizing interactive.message")

	data, ok := msgEvents[0].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", data["role"])
	assert.Equal(t, "Hello world", data["content"])

	// Ordering invariant: the interactive.message must land at-or-after the
	// waiting transition. A future refactor that moves the Publish back above
	// UpdateState(waiting) reintroduces the publish-before-persist race and
	// fails here loudly (no clock, no sleep).
	require.GreaterOrEqual(t, waitingSnapshot, 0, "onUpdateState(waiting) must have fired")
	msgIndex := eventBus.indexOfType(instanceID, "interactive.message")
	require.GreaterOrEqual(t, msgIndex, 0, "interactive.message must be present in the ordered event log")
	assert.GreaterOrEqual(t, msgIndex, waitingSnapshot,
		"interactive.message must be published at-or-after UpdateState(waiting)")
}

func TestInteractiveExecutor_WritesNodeExecutionRecord(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Full", " response"}, 10, 20)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	require.Len(t, execs, 1)

	exec := execs[0]
	assert.Equal(t, instance.NodeStatusSucceeded, exec.Status)
	assert.Equal(t, "interact", exec.NodeID)
	assert.Equal(t, instanceID, exec.InstanceID)
	assert.False(t, exec.StartedAt.IsZero(), "StartedAt must be set on the node execution record")
	assert.NotNil(t, exec.CompletedAt)

	// Conversation must be persisted with the correct user+assistant structure.
	// If the executor stops writing conversation data, the ConversationHandler
	// cannot load history on reconnect, silently breaking multi-turn sessions.
	require.NotNil(t, exec.LLMConversation)
	messages, ok := exec.LLMConversation["messages"].([]map[string]any)
	require.True(t, ok, "conversation.messages must be []map[string]any")
	require.Len(t, messages, 2, "conversation must have exactly user + assistant messages")
	assert.Equal(t, "user", messages[0]["role"])
	assert.Equal(t, "assistant", messages[1]["role"])

	// LLM metadata must be persisted so the execution view can display model/token info.
	require.NotNil(t, exec.LLMModel, "LLMModel must be set on the node execution record")
	assert.Equal(t, "claude-sonnet-4-20250514", *exec.LLMModel)
	require.NotNil(t, exec.LLMTokensIn, "LLMTokensIn must be set on the node execution record")
	assert.Equal(t, 10, *exec.LLMTokensIn)
	require.NotNil(t, exec.LLMTokensOut, "LLMTokensOut must be set on the node execution record")
	assert.Equal(t, 20, *exec.LLMTokensOut)
}

// Notification tests (CreatesNotification, CallsExternalNotifier,
// ExternalNotifier_NotificationHasNonZeroCreatedAt, PublishesNotificationCreatedEvent)
// were removed because notification creation is now handled by the engine
// when it processes the IsWait result from the executor.

func TestInteractiveExecutor_TransitionsToWaiting(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

func TestInteractiveExecutor_NotifiesCapacityFreed(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	select {
	case <-queueMgr.notifyCapacityFreedCh:
		// success
	case <-time.After(time.Second):
		t.Fatal("expected NotifyCapacityFreed to be called")
	}
}

func TestInteractiveExecutor_ReturnsIsWait(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)
}

func TestInteractiveExecutor_ReturnsLLMMetadata(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Response"}, 42, 84)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "claude-sonnet-4-20250514", result.LLMModel)
	assert.Equal(t, 42, result.LLMTokensIn)
	assert.Equal(t, 84, result.LLMTokensOut)
	assert.NotNil(t, result.LLMConversation)
}

func TestInteractiveExecutor_UserMessageCombinesOrchestrationAndTemplate(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Step 1: Do this\nStep 2: Do that")
	fileReader.files["test-wf/template.md"] = []byte("Name: Bob\nAge: 30")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)

	// User message = orchestration + "\n\n" + resolved template, followed by
	// the completion-sentinel instruction (with per-instance nonce).
	assert.Contains(t, reqs[0].Prompt, "Step 1: Do this\nStep 2: Do that\n\nName: Bob\nAge: 30")
	assert.Contains(t, reqs[0].Prompt, "<arbitron:result:")
}

// ---------------------------------------------------------------------------
// Tests: Error cases
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_NoInstanceIDInContext_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, _ := interactiveTestSetup()

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	// No instance ID embedded in context.
	_, err := executor.Execute(context.Background(), interactiveNode(), variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance ID not found in context")
}

func TestInteractiveExecutor_OrchestrationFileNotFound_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	// Only template file registered, no orchestration file
	fileReader.files["test-wf/template.md"] = []byte("Template")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "orchestration")
}

func TestInteractiveExecutor_TemplateFileNotFound_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	// Only orchestration file registered, no template file
	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "template")
}

func TestInteractiveExecutor_StreamChatError_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		return nil, fmt.Errorf("LLM service unavailable")
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM service unavailable")
}

func TestInteractiveExecutor_StreamChunkError_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, 3)
		ch <- engine.ChatChunk{Token: "partial"}
		ch <- engine.ChatChunk{Error: fmt.Errorf("connection reset")}
		close(ch)
		return ch, nil
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset")
}

func TestInteractiveExecutor_UpdateStateFailure_ReturnsError(t *testing.T) {
	// Use an instance repo that does NOT contain our instance ID so that
	// UpdateState returns "instance not found". Every earlier step (file reads,
	// LLM call, nodeExec.Create) succeeds — only the state transition fails,
	// which must propagate as an error.
	_, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, _ := interactiveTestSetup()

	emptyInstRepo := newMockInstanceRepo() // no instances registered

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")
	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := engine.NewInteractiveExecutor(
		emptyInstRepo, nodeExecRepo, streamSelector(llmProvider), interactiveResolver(), fileReader, eventBus, queueMgr,
	)

	instanceID := uuid.New()
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to set waiting state")
}

func TestInteractiveExecutor_AgentProfile_UsesAgentStreamAndBindingModel(t *testing.T) {
	// An interactive_agent node whose model_profile resolves to an AGENT binding
	// must stream via the agent (tool-enabled) provider, and the request model
	// must come from binding.Model — not from node.Config.Model.
	instRepo, nodeExecRepo, _, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	chatProv := newMockStreamingChatProvider()
	agentProv := newMockStreamingChatProvider()
	agentProv.streamCallFn = cannedStream([]string{"agent reply"}, 7, 9)

	resolver := newFakeResolver(provider.Profile{
		Alias: "agent-prof",
		Kind:  provider.ProfileKindAgent,
		Model: "claude-opus-agent",
	})
	sel := &fakeStreamSelector{chatProv: chatProv, agentProv: agentProv}

	executor := engine.NewInteractiveExecutor(
		instRepo, nodeExecRepo, sel, resolver, fileReader, eventBus, queueMgr,
	)

	node := interactiveNode()
	node.Type = workflow.NodeTypeInteractiveAgent
	node.Config.ModelProfile = "agent-prof"

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")
	require.NoError(t, err)
	require.NotNil(t, result)

	// Selector saw the agent binding's model.
	assert.Equal(t, provider.ProfileKindAgent, sel.gotKind)
	assert.Equal(t, "claude-opus-agent", sel.gotModel)

	// The agent stream provider was used (not the chat provider).
	assert.Empty(t, chatProv.getCapturedReqs(), "chat provider must not be used for an agent profile")
	agentReqs := agentProv.getCapturedReqs()
	require.Len(t, agentReqs, 1)
	assert.Equal(t, "claude-opus-agent", agentReqs[0].Model, "request model must come from binding.Model")

	// Persisted model reflects the resolved binding model.
	assert.Equal(t, "claude-opus-agent", result.LLMModel)
}

func TestInteractiveExecutor_UnknownProfile_ResolveErrorSurfaces(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	// Resolver knows nothing -> Resolve returns ErrUnknownProfile.
	executor := engine.NewInteractiveExecutor(
		instRepo, nodeExecRepo, streamSelector(llmProvider), newFakeResolver(), fileReader, eventBus, queueMgr,
	)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
	assert.Contains(t, err.Error(), "resolve profile")
	// LLM must not be called when the profile cannot be resolved.
	assert.Empty(t, llmProvider.getCapturedReqs())
}

func TestInteractiveExecutor_SelectStreamProviderError_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	sel := &fakeStreamSelector{err: fmt.Errorf("no stream backend")}

	executor := engine.NewInteractiveExecutor(
		instRepo, nodeExecRepo, sel, interactiveResolver(), fileReader, eventBus, queueMgr,
	)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "select stream provider")
	assert.Empty(t, llmProvider.getCapturedReqs())
}

func TestInteractiveExecutor_AccumulatesFullResponse(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " ", "World", "!"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the conversation contains the accumulated response
	conv, ok := result.LLMConversation.(map[string]any)
	require.True(t, ok)
	messages, ok := conv["messages"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	assert.Equal(t, "assistant", messages[1]["role"])
	assert.Equal(t, "Hello World!", messages[1]["content"])
}

func TestInteractiveExecutor_CapturesSessionIDAndWorkDir(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")
	llmProvider.streamCallFn = cannedStreamWithSession([]string{"hello"}, "isess-1")

	vars := variable.NewStore()
	// "workspace" is engine-reserved; use SetReserved to bypass the runtime
	// mutability check (SF12).
	vars.SetReserved("workspace", "/tmp/ws")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)
	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), vars, "test-wf")
	require.NoError(t, err)

	// WorkDir flows from the seeded {{workspace}} variable; sentinel present.
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/tmp/ws", reqs[0].WorkDir)
	assert.Contains(t, reqs[0].Prompt, "<arbitron:result:")

	// Session id is persisted under LLMConversation["sessionId"] for resumption.
	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	require.Len(t, execs, 1)
	assert.Equal(t, "isess-1", execs[0].LLMConversation["sessionId"])
}

// ---------------------------------------------------------------------------
// Tests: Boundary / edge cases
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_EmptyStreamTokensSkipped(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	// Stream with empty token (should still publish events for non-empty tokens)
	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, 4)
		ch <- engine.ChatChunk{Token: "Hello"}
		ch <- engine.ChatChunk{Token: ""}
		ch <- engine.ChatChunk{Token: "World"}
		ch <- engine.ChatChunk{Done: true, TokensIn: 5, TokensOut: 10}
		close(ch)
		return ch, nil
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)

	// Only non-empty tokens should produce events
	tokenEvents := eventBus.eventsOfType(instanceID, "interactive.token")
	assert.Len(t, tokenEvents, 2)
}

func TestInteractiveExecutor_NoOutputSchema_NoTools(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	node := interactiveNode()
	node.Config.OutputSchema = nil // No output schema

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")

	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	// SF8: Tools field removed; no assertion needed.
	_ = reqs[0]
}

func TestInteractiveExecutor_NoNotificationDeps_DoesNotPanic(t *testing.T) {
	// Notification creation is now handled by the engine when it processes
	// the IsWait result. The executor must work without notification deps.
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")
	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := engine.NewInteractiveExecutor(
		instRepo, nodeExecRepo, streamSelector(llmProvider), interactiveResolver(), fileReader, eventBus, queueMgr,
	)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

// TestInteractiveExecutor_ExternalNotifier_ErrorDoesNotAbortExecution was
// removed because notification creation is now handled by the engine.

// ---------------------------------------------------------------------------
// Tests: IsWait resumption idempotency
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_ResumeAfterSessionComplete_ReturnsExistingOutput(t *testing.T) {
	// Integration: after ConversationHandler completes the session, the queue
	// re-starts the instance with CurrentNodeID still pointing at the interactive
	// node. The executor must detect the existing NodeStatusSucceeded record and
	// return the stored output without starting a new LLM conversation.
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	// Pre-seed the completed node execution record that ConversationHandler leaves.
	existingOutput := map[string]any{"summary": "session concluded"}
	completedExec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "interact",
		Attempt:    1,
		Status:     instance.NodeStatusSucceeded,
		Output:     existingOutput,
	}
	require.NoError(t, nodeExecRepo.Create(context.Background(), completedExec))

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)

	// Must not re-enter the wait state.
	assert.False(t, result.IsWait, "re-execution must not issue a new wait")

	// Must return the output the conversation handler persisted.
	assert.Equal(t, existingOutput, result.Output)

	// LLM must not be called on resume — no new conversation should start.
	assert.Empty(t, llmProvider.getCapturedReqs(), "LLM must not be called on resume")

	// Executor must not create a second node execution record.
	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	assert.Len(t, execs, 1, "executor must not duplicate the node execution record on resume")

	// Instance state must not be driven back to waiting.
	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateRunning, inst.State)
}

// ---------------------------------------------------------------------------
// Tests: binding.Kind <-> node.Type guard (Item 6, comment 4614897... /
// executor_interactive.go:112). The InteractiveExecutor is registered under
// BOTH interactive_llm and interactive_agent and routes purely on binding.Kind.
// A snapshot loaded from a pre-validation workflow, or a programmatically built
// node, can present a node whose Type disagrees with the resolved binding.Kind.
// The two paths have OPPOSITE tool postures (chat = tool-suppressed, agent =
// YOLO/--dangerously-skip-permissions), so a mismatch is a config-driven
// privilege escalation. The executor must fail closed at the Execute boundary
// with a hard, named error — not depend on upstream validation having run.
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_KindTypeMismatch_AgentNodeChatBinding_FailsClosed(t *testing.T) {
	instRepo, nodeExecRepo, _, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	chatProv := newMockStreamingChatProvider()
	chatProv.streamCallFn = cannedStream([]string{"should not run"}, 1, 1)
	agentProv := newMockStreamingChatProvider()
	agentProv.streamCallFn = cannedStream([]string{"should not run"}, 1, 1)

	// Profile resolves to a CHAT binding, but the node is interactive_agent.
	resolver := newFakeResolver(provider.Profile{
		Alias: "mismatch-prof",
		Kind:  provider.ProfileKindChat,
		Model: "claude-sonnet",
	})
	sel := &fakeStreamSelector{chatProv: chatProv, agentProv: agentProv}

	executor := engine.NewInteractiveExecutor(
		instRepo, nodeExecRepo, sel, resolver, fileReader, eventBus, queueMgr,
	)

	node := interactiveNode()
	node.Type = workflow.NodeTypeInteractiveAgent
	node.Config.ModelProfile = "mismatch-prof"

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	// No provider may have been invoked: the guard fails closed before streaming.
	assert.Empty(t, chatProv.getCapturedReqs(), "no stream may start on a kind/type mismatch")
	assert.Empty(t, agentProv.getCapturedReqs(), "no stream may start on a kind/type mismatch")
	// No node execution record may be persisted as succeeded.
	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	assert.Empty(t, execs, "no node execution record may be written on a kind/type mismatch")
}

func TestInteractiveExecutor_KindTypeMismatch_LLMNodeAgentBinding_FailsClosed(t *testing.T) {
	instRepo, nodeExecRepo, _, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	chatProv := newMockStreamingChatProvider()
	chatProv.streamCallFn = cannedStream([]string{"should not run"}, 1, 1)
	agentProv := newMockStreamingChatProvider()
	agentProv.streamCallFn = cannedStream([]string{"should not run"}, 1, 1)

	// Profile resolves to an AGENT binding (YOLO posture), but the node is
	// declared interactive_llm (expects tool-suppressed chat). This is the
	// chat->RCE escalation the security reviewer flagged.
	resolver := newFakeResolver(provider.Profile{
		Alias: "escalate-prof",
		Kind:  provider.ProfileKindAgent,
		Model: "claude-opus-agent",
	})
	sel := &fakeStreamSelector{chatProv: chatProv, agentProv: agentProv}

	executor := engine.NewInteractiveExecutor(
		instRepo, nodeExecRepo, sel, resolver, fileReader, eventBus, queueMgr,
	)

	node := interactiveNode() // Type is interactive_llm
	node.Config.ModelProfile = "escalate-prof"

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.Empty(t, chatProv.getCapturedReqs(), "no stream may start on a kind/type mismatch")
	assert.Empty(t, agentProv.getCapturedReqs(), "no stream may start on a kind/type mismatch")
}

// ---------------------------------------------------------------------------
// Tests: stream closed without a Done chunk (Item 5, comment 4614891622 /
// executor_interactive.go:141-166). On mid-stream context cancellation
// (instance cancel, shutdown, timeout) makeSendChunk drops the terminal Done
// chunk and the channel closes silently. A turn that was actually cancelled or
// truncated must NOT be recorded as NodeStatusSucceeded; the executor must
// return an error so the conversation does not advance on partial data.
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_StreamClosesWithoutDone_ReturnsError(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	// Stream emits a partial token then closes WITHOUT a Done chunk and WITHOUT
	// an error chunk — exactly the makeSendChunk ctx-cancel drop path.
	llmProvider.streamCallFn = func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Token: "partial"}
		close(ch)
		return ch, nil
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err, "a stream that closes without a Done chunk must be an error, not a success")
	assert.Contains(t, err.Error(), "stream closed without completion")

	// The node must NOT be persisted as succeeded on a truncated turn.
	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	for _, e := range execs {
		assert.NotEqual(t, instance.NodeStatusSucceeded, e.Status,
			"a truncated/cancelled turn must not be recorded as a completed success")
	}
}
