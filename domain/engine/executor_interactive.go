package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Compile-time assertion that *InteractiveExecutor satisfies NodeExecutor.
var _ NodeExecutor = (*InteractiveExecutor)(nil)

// InteractiveExecutor handles interactive nodes by initiating an LLM
// conversation, streaming the first response, and transitioning the instance
// to the waiting state. Notification creation is handled by the engine after
// the executor returns.
type InteractiveExecutor struct {
	instances   instance.InstanceRepository
	nodeExecs   instance.NodeExecutionRepository
	llmProvider LLMProvider
	fileReader  FileReader
	eventBus    EventBus
	queueMgr    QueueManager
}

// NewInteractiveExecutor constructs a new InteractiveExecutor with all required dependencies.
func NewInteractiveExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	llmProvider LLMProvider,
	fileReader FileReader,
	eventBus EventBus,
	queueMgr QueueManager,
) *InteractiveExecutor {
	return &InteractiveExecutor{
		instances:   instances,
		nodeExecs:   nodeExecs,
		llmProvider: llmProvider,
		fileReader:  fileReader,
		eventBus:    eventBus,
		queueMgr:    queueMgr,
	}
}

// Execute reads the orchestration and template files, resolves variable
// substitutions, streams the first LLM response, writes the node execution
// record, transitions the instance to waiting, and returns a wait result.
func (e *InteractiveExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	// 1. Extract instance ID from context.
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("interactive executor: instance ID not found in context")
	}

	// 2. Short-circuit if the session was already completed by ConversationHandler.
	// After an interactive session ends, the queue re-starts the instance with
	// CurrentNodeID still pointing at this node. Returning the stored output here
	// lets the engine advance to the next node without starting a new conversation.
	if existing, err := e.nodeExecs.GetByInstanceAndNode(ctx, instanceID, node.ID); err == nil &&
		existing.Status == instance.NodeStatusSucceeded {
		return &NodeResult{Output: existing.Output}, nil
	}

	startTime := time.Now()

	// 3. Read the orchestration file.
	orchBytes, err := e.fileReader.ReadFile(workflowName, node.Config.OrchestrationFile)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: read orchestration: %w", node.ID, err)
	}

	// 4. Read the template file.
	templateBytes, err := e.fileReader.ReadFile(workflowName, node.Config.TemplateFile)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: read template: %w", node.ID, err)
	}

	// 5. Resolve {{variable}} substitutions in the template content.
	resolvedTemplate := variable.Resolve(string(templateBytes), vars)

	// 6. Compose the LLM request. The completion-sentinel instruction is
	// appended to the prompt instead of injecting a complete_session tool, and
	// the working directory is read from the {{workspace}} variable.
	workDir, _ := vars.Get("workspace")
	workDirStr, _ := workDir.(string)

	userMessage := string(orchBytes) + "\n\n" + resolvedTemplate +
		sentinelInstruction(node.Config.OutputSchema)

	req := LLMRequest{
		SystemPrompt: node.Config.SystemPrompt,
		Prompt:       userMessage,
		Model:        node.Config.Model,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		WorkDir:      workDirStr,
	}

	// 7. Call the LLM provider with streaming.
	stream, err := e.llmProvider.StreamCall(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: stream call: %w", node.ID, err)
	}

	// 8. Consume the stream channel.
	var responseBuilder strings.Builder
	var tokensIn, tokensOut int
	var sessionID string

	for chunk := range stream {
		if chunk.Error != nil {
			return nil, fmt.Errorf("interactive %q: stream error: %w", node.ID, chunk.Error)
		}

		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}

		if chunk.Token != "" {
			responseBuilder.WriteString(chunk.Token)
			e.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": chunk.Token},
			})
		}

		if chunk.Done {
			tokensIn = chunk.TokensIn
			tokensOut = chunk.TokensOut
			break
		}
	}

	fullResponse := responseBuilder.String()

	// 9. Build the initial conversation array.
	conversation := map[string]any{
		"sessionId": sessionID,
		"messages": []map[string]any{
			{"role": "user", "content": userMessage},
			{"role": "assistant", "content": fullResponse},
		},
	}

	// 10. Write node execution record with succeeded status.
	now := time.Now()
	model := node.Config.Model
	nodeExec := &instance.NodeExecution{
		ID:              uuid.New(),
		InstanceID:      instanceID,
		NodeID:          node.ID,
		Attempt:         1,
		Status:          instance.NodeStatusSucceeded,
		LLMConversation: conversation,
		LLMModel:        &model,
		LLMTokensIn:     &tokensIn,
		LLMTokensOut:    &tokensOut,
		StartedAt:       startTime,
		CompletedAt:     &now,
	}
	if err := e.nodeExecs.Create(ctx, nodeExec); err != nil {
		slog.Error("interactive: failed to write node execution record", "instanceID", instanceID, "error", err)
	}

	// 11. Transition instance to waiting state.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		return nil, fmt.Errorf("interactive %q: failed to set waiting state: %w", node.ID, err)
	}

	// 12. Notify capacity freed.
	e.queueMgr.NotifyCapacityFreed(ctx)

	// 13. Return result with IsWait=true.
	return &NodeResult{
		IsWait:          true,
		LLMConversation: conversation,
		LLMModel:        model,
		LLMTokensIn:     tokensIn,
		LLMTokensOut:    tokensOut,
	}, nil
}
