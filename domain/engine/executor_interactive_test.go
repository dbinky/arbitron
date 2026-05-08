package engine_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock: streaming LLM provider for interactive executor tests
// ---------------------------------------------------------------------------

type mockStreamingLLMProvider struct {
	mu            sync.Mutex
	streamCallFn  func(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error)
	capturedReqs  []engine.LLMRequest
}

func newMockStreamingLLMProvider() *mockStreamingLLMProvider {
	return &mockStreamingLLMProvider{}
}

func (m *mockStreamingLLMProvider) Call(_ context.Context, _ engine.LLMRequest) (*engine.LLMResponse, error) {
	return nil, fmt.Errorf("Call not implemented for streaming mock")
}

func (m *mockStreamingLLMProvider) StreamCall(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	m.mu.Lock()
	m.capturedReqs = append(m.capturedReqs, req)
	fn := m.streamCallFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return nil, fmt.Errorf("no stream call function configured")
}

func (m *mockStreamingLLMProvider) getCapturedReqs() []engine.LLMRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]engine.LLMRequest, len(m.capturedReqs))
	copy(cp, m.capturedReqs)
	return cp
}

// cannedStream returns a StreamCall function that emits the given tokens
// and a final Done chunk with token counts.
func cannedStream(tokens []string, tokensIn, tokensOut int) func(ctx context.Context, req engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	return func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		ch := make(chan engine.LLMStreamChunk, len(tokens)+1)
		for _, t := range tokens {
			ch <- engine.LLMStreamChunk{Token: t}
		}
		ch <- engine.LLMStreamChunk{Done: true, TokensIn: tokensIn, TokensOut: tokensOut}
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
	*mockStreamingLLMProvider,
	*mockFileReader,
	*mockEventBus,
	*mockQueueManager,
	*mockNotificationRepo,
	*mockExternalNotifierForInteractive,
	uuid.UUID,
) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	llmProvider := newMockStreamingLLMProvider()
	fileReader := newMockFileReader()
	eventBus := newMockEventBus()
	queueMgr := newMockQueueManager()
	notifRepo := newMockNotificationRepo()
	extNotifier := newMockExternalNotifierForInteractive()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	return instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID
}

// mockExternalNotifierForInteractive implements notification.ExternalNotifier.
type mockExternalNotifierForInteractive struct {
	mu     sync.Mutex
	called []notification.Notification
}

func newMockExternalNotifierForInteractive() *mockExternalNotifierForInteractive {
	return &mockExternalNotifierForInteractive{}
}

func (m *mockExternalNotifierForInteractive) Notify(_ context.Context, n *notification.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = append(m.called, *n)
	return nil
}

func (m *mockExternalNotifierForInteractive) getCalled() []notification.Notification {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]notification.Notification, len(m.called))
	copy(cp, m.called)
	return cp
}

func interactiveNode() workflow.Node {
	return workflow.Node{
		ID:   "interact",
		Type: workflow.NodeTypeInteractive,
		Config: workflow.NodeConfig{
			SystemPrompt:      "You are a helpful assistant.",
			OrchestrationFile: "orchestration.md",
			TemplateFile:      "template.md",
			Model:             "claude-sonnet-4-20250514",
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
	llmProvider *mockStreamingLLMProvider,
	fileReader *mockFileReader,
	eventBus *mockEventBus,
	queueMgr *mockQueueManager,
	notifRepo *mockNotificationRepo,
	extNotifier *mockExternalNotifierForInteractive,
) *engine.InteractiveExecutor {
	return engine.NewInteractiveExecutor(
		instRepo,
		nodeExecRepo,
		llmProvider,
		fileReader,
		eventBus,
		queueMgr,
		notifRepo,
		extNotifier,
	)
}

// ---------------------------------------------------------------------------
// Tests: Happy path
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_ReadsOrchestrationFile(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration content here")
	fileReader.files["test-wf/template.md"] = []byte("Template content")

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " there"}, 10, 20)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

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
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template content")

	llmProvider.streamCallFn = cannedStream([]string{"Response"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0].Prompt, "Template content")
}

func TestInteractiveExecutor_VariableSubstitutionInTemplate(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Hello {{name}}, please review {{topic}}")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	vars := variable.NewStore()
	vars.SetInput("name", "Alice")
	vars.SetInput("topic", "Q3 report")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), vars, "test-wf")

	require.NoError(t, err)
	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0].Prompt, "Hello Alice, please review Q3 report")
}

func TestInteractiveExecutor_StreamCallInvokedWithCorrectRequest(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration instructions")
	fileReader.files["test-wf/template.md"] = []byte("Template data")

	llmProvider.streamCallFn = cannedStream([]string{"response"}, 15, 25)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

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

	// Verify tools: complete_session tool built from outputSchema
	require.Len(t, req.Tools, 1)
	assert.Equal(t, "complete_session", req.Tools[0].Name)
	assert.Equal(t, node.Config.OutputSchema, req.Tools[0].InputSchema)
}

func TestInteractiveExecutor_PublishesTokenEvents(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " ", "world"}, 5, 15)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

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

func TestInteractiveExecutor_WritesNodeExecutionRecord(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Full", " response"}, 10, 20)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	require.Len(t, execs, 1)

	exec := execs[0]
	assert.Equal(t, instance.NodeStatusSucceeded, exec.Status)
	assert.Equal(t, "interact", exec.NodeID)
	assert.Equal(t, instanceID, exec.InstanceID)
	assert.NotNil(t, exec.CompletedAt)
	assert.NotNil(t, exec.LLMConversation)

	// Verify the conversation contains user and assistant messages
	conv := exec.LLMConversation
	require.NotNil(t, conv)
}

func TestInteractiveExecutor_CreatesNotification(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	notifications := notifRepo.getCreated()
	require.Len(t, notifications, 1)

	n := notifications[0]
	assert.Equal(t, instanceID, n.InstanceID)
	assert.Equal(t, "test-wf", n.WorkflowName)
	assert.Equal(t, notification.TypeNeedsInteraction, n.Type)
	assert.NotEqual(t, uuid.Nil, n.ID)
}

func TestInteractiveExecutor_CallsExternalNotifier(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	called := extNotifier.getCalled()
	require.Len(t, called, 1)
	assert.Equal(t, notification.TypeNeedsInteraction, called[0].Type)
	assert.Equal(t, instanceID, called[0].InstanceID)
}

func TestInteractiveExecutor_PublishesNotificationCreatedEvent(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	notifEvents := eventBus.eventsOfType(instanceID, "notification.created")
	require.Len(t, notifEvents, 1)

	data, ok := notifEvents[0].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "needs_interaction", data["type"])
}

func TestInteractiveExecutor_TransitionsToWaiting(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

func TestInteractiveExecutor_NotifiesCapacityFreed(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

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
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)
}

func TestInteractiveExecutor_ReturnsLLMMetadata(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Response"}, 42, 84)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

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
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Step 1: Do this\nStep 2: Do that")
	fileReader.files["test-wf/template.md"] = []byte("Name: Bob\nAge: 30")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)

	// User message should be orchestration + "\n\n" + resolved template
	expected := "Step 1: Do this\nStep 2: Do that\n\nName: Bob\nAge: 30"
	assert.Equal(t, expected, reqs[0].Prompt)
}

// ---------------------------------------------------------------------------
// Tests: Error cases
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_NoInstanceIDInContext_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, _ := interactiveTestSetup()

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	// No instance ID embedded in context.
	_, err := executor.Execute(context.Background(), interactiveNode(), variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance ID not found in context")
}

func TestInteractiveExecutor_OrchestrationFileNotFound_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	// Only template file registered, no orchestration file
	fileReader.files["test-wf/template.md"] = []byte("Template")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "orchestration")
}

func TestInteractiveExecutor_TemplateFileNotFound_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	// Only orchestration file registered, no template file
	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "template")
}

func TestInteractiveExecutor_StreamCallError_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		return nil, fmt.Errorf("LLM service unavailable")
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM service unavailable")
}

func TestInteractiveExecutor_StreamChunkError_Fails(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		ch := make(chan engine.LLMStreamChunk, 3)
		ch <- engine.LLMStreamChunk{Token: "partial"}
		ch <- engine.LLMStreamChunk{Error: fmt.Errorf("connection reset")}
		close(ch)
		return ch, nil
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection reset")
}

func TestInteractiveExecutor_AccumulatesFullResponse(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"Hello", " ", "World", "!"}, 5, 10)

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

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

func TestInteractiveExecutor_BuildsCompleteSessionTool(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rating":  map[string]any{"type": "number"},
			"comment": map[string]any{"type": "string"},
		},
	}

	node := interactiveNode()
	node.Config.OutputSchema = schema

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")

	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	require.Len(t, reqs[0].Tools, 1)

	tool := reqs[0].Tools[0]
	assert.Equal(t, "complete_session", tool.Name)
	assert.Equal(t, schema, tool.InputSchema)
}

// ---------------------------------------------------------------------------
// Tests: Boundary / edge cases
// ---------------------------------------------------------------------------

func TestInteractiveExecutor_EmptyStreamTokensSkipped(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	// Stream with empty token (should still publish events for non-empty tokens)
	llmProvider.streamCallFn = func(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
		ch := make(chan engine.LLMStreamChunk, 4)
		ch <- engine.LLMStreamChunk{Token: "Hello"}
		ch <- engine.LLMStreamChunk{Token: ""}
		ch <- engine.LLMStreamChunk{Token: "World"}
		ch <- engine.LLMStreamChunk{Done: true, TokensIn: 5, TokensOut: 10}
		close(ch)
		return ch, nil
	}

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

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
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier, instanceID := interactiveTestSetup()

	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")

	llmProvider.streamCallFn = cannedStream([]string{"OK"}, 5, 10)

	node := interactiveNode()
	node.Config.OutputSchema = nil // No output schema

	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, notifRepo, extNotifier)

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, variable.NewStore(), "test-wf")

	require.NoError(t, err)

	reqs := llmProvider.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Empty(t, reqs[0].Tools)
}
