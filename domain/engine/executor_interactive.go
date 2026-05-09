package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Compile-time assertion that *InteractiveExecutor satisfies NodeExecutor.
var _ NodeExecutor = (*InteractiveExecutor)(nil)

// InteractiveExecutor handles interactive nodes by initiating an LLM
// conversation, streaming the first response, creating a needs_interaction
// notification, and transitioning the instance to the waiting state.
type InteractiveExecutor struct {
	instances        instance.InstanceRepository
	nodeExecs        instance.NodeExecutionRepository
	llmProvider      LLMProvider
	fileReader       FileReader
	eventBus         EventBus
	queueMgr         QueueManager
	notifications    notification.Repository
	externalNotifier notification.ExternalNotifier
}

// NewInteractiveExecutor constructs a new InteractiveExecutor with all required dependencies.
func NewInteractiveExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	llmProvider LLMProvider,
	fileReader FileReader,
	eventBus EventBus,
	queueMgr QueueManager,
	notifications notification.Repository,
	externalNotifier notification.ExternalNotifier,
) *InteractiveExecutor {
	return &InteractiveExecutor{
		instances:        instances,
		nodeExecs:        nodeExecs,
		llmProvider:      llmProvider,
		fileReader:       fileReader,
		eventBus:         eventBus,
		queueMgr:         queueMgr,
		notifications:    notifications,
		externalNotifier: externalNotifier,
	}
}

// Execute reads the orchestration and template files, resolves variable
// substitutions, streams the first LLM response, writes the node execution
// record, creates a needs_interaction notification, transitions the instance
// to waiting, and returns a wait result.
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

	// 6. Build the complete_session tool definition from outputSchema.
	var tools []LLMTool
	if node.Config.OutputSchema != nil {
		tools = append(tools, LLMTool{
			Name:        InteractiveCompletionToolName,
			Description: InteractiveCompletionToolDescription,
			InputSchema: node.Config.OutputSchema,
		})
	}

	// 7. Compose the LLM request.
	userMessage := string(orchBytes) + "\n\n" + resolvedTemplate
	req := LLMRequest{
		SystemPrompt: node.Config.SystemPrompt,
		Prompt:       userMessage,
		Model:        node.Config.Model,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		Tools:        tools,
	}

	// 8. Call the LLM provider with streaming.
	stream, err := e.llmProvider.StreamCall(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: stream call: %w", node.ID, err)
	}

	// 9. Consume the stream channel.
	var responseBuilder strings.Builder
	var tokensIn, tokensOut int

	for chunk := range stream {
		if chunk.Error != nil {
			return nil, fmt.Errorf("interactive %q: stream error: %w", node.ID, chunk.Error)
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

	// 10. Build the initial conversation array.
	conversation := map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": userMessage},
			{"role": "assistant", "content": fullResponse},
		},
	}

	// 11. Write node execution record with succeeded status.
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

	// 12. Create notification.
	n := &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   instanceID,
		WorkflowName: workflowName,
		Type:         notification.TypeNeedsInteraction,
		CreatedAt:    time.Now(),
	}

	// 13. Persist notification.
	if err := e.notifications.Create(ctx, n); err != nil {
		slog.Error("interactive: failed to create notification", "instanceID", instanceID, "error", err)
	}

	// 14. Call external notifier.
	if e.externalNotifier != nil {
		if err := e.externalNotifier.Notify(ctx, n); err != nil {
			slog.Error("interactive: external notifier failed", "instanceID", instanceID, "error", err)
		}
	}

	// 15. Publish notification.created event.
	e.eventBus.Publish(instanceID, Event{
		Type:      "notification.created",
		Timestamp: time.Now(),
		Data: map[string]any{
			"id":           n.ID.String(),
			"instanceId":   n.InstanceID.String(),
			"workflowName": n.WorkflowName,
			"type":         string(n.Type),
			"read":         n.Read,
			"dismissed":    n.Dismissed,
			"createdAt":    n.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
		},
	})

	// 16. Transition instance to waiting state.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		return nil, fmt.Errorf("interactive %q: failed to set waiting state: %w", node.ID, err)
	}

	// 17. Notify capacity freed.
	e.queueMgr.NotifyCapacityFreed(ctx)

	// 18. Return result with IsWait=true.
	return &NodeResult{
		IsWait:          true,
		LLMConversation: conversation,
		LLMModel:        model,
		LLMTokensIn:     tokensIn,
		LLMTokensOut:    tokensOut,
	}, nil
}
