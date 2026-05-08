package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ConversationHandler is a domain service that processes user messages during
// an active interactive session, manages the LLM conversation loop, detects
// session completion via tool-use, and handles user-initiated session end.
type ConversationHandler interface {
	HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error
	EndSession(ctx context.Context, instanceID uuid.UUID) error
}

// conversationHandler implements ConversationHandler.
type conversationHandler struct {
	instances   instance.InstanceRepository
	nodeExecs   instance.NodeExecutionRepository
	llmProvider LLMProvider
	eventBus    EventBus
	queueMgr    QueueManager
}

// NewConversationHandler constructs a new ConversationHandler with all required
// dependencies.
func NewConversationHandler(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	llmProvider LLMProvider,
	eventBus EventBus,
	queueMgr QueueManager,
) ConversationHandler {
	return &conversationHandler{
		instances:   instances,
		nodeExecs:   nodeExecs,
		llmProvider: llmProvider,
		eventBus:    eventBus,
		queueMgr:    queueMgr,
	}
}

// HandleMessage processes a user message in an active interactive session.
// It loads the instance and node execution, appends the user message to the
// conversation, calls the LLM with the full history, streams the response,
// and handles tool calls for session completion.
func (h *conversationHandler) HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error {
	// 1. Load and validate instance state.
	inst, nodeID, nodeConfig, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}
	_ = inst // used for variable updates on completion

	// 2. Load node execution record.
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, instanceID, nodeID)
	if err != nil {
		return fmt.Errorf("conversation: load node execution: %w", err)
	}

	// 3. Parse conversation history.
	messages := parseConversation(nodeExec.LLMConversation)

	// 4. Append user message.
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": content,
	})

	// 5. Persist updated conversation immediately (durability before LLM call).
	nodeExec.LLMConversation = map[string]any{"messages": messages}
	if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
		return fmt.Errorf("conversation: persist user message: %w", err)
	}

	// 6. Publish interactive.message event for user message.
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveMessage,
		Timestamp: time.Now(),
		Data: map[string]any{
			"role":    "user",
			"content": content,
		},
	})

	// 7. Build the complete_session tool from output schema.
	var tools []LLMTool
	if nodeConfig.OutputSchema != nil {
		tools = append(tools, LLMTool{
			Name:        "complete_session",
			Description: "Complete the interactive session with structured output.",
			InputSchema: nodeConfig.OutputSchema,
		})
	}

	// 8. Build the LLM request with full conversation history.
	llmMessages := make([]LLMMessage, len(messages))
	for i, msg := range messages {
		role, _ := msg["role"].(string)
		msgContent, _ := msg["content"].(string)
		llmMessages[i] = LLMMessage{Role: role, Content: msgContent}
	}

	req := LLMRequest{
		SystemPrompt: nodeConfig.SystemPrompt,
		Messages:     llmMessages,
		Model:        nodeConfig.Model,
		MaxTokens:    nodeConfig.MaxTokens,
		Temperature:  nodeConfig.Temperature,
		Tools:        tools,
	}

	// 9. Call the LLM provider with streaming.
	stream, err := h.llmProvider.StreamCall(ctx, req)
	if err != nil {
		return fmt.Errorf("conversation: stream call: %w", err)
	}

	// 10. Consume the stream.
	var responseBuilder strings.Builder
	var toolUse *LLMToolUse

	for chunk := range stream {
		if chunk.Error != nil {
			return fmt.Errorf("conversation: stream error: %w", chunk.Error)
		}

		if chunk.Token != "" {
			responseBuilder.WriteString(chunk.Token)
			h.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": chunk.Token},
			})
		}

		if chunk.ToolUse != nil {
			toolUse = chunk.ToolUse
		}

		if chunk.Done {
			break
		}
	}

	fullResponse := responseBuilder.String()

	// 11. Handle the response based on whether a tool was called.
	if toolUse != nil && toolUse.Name == "complete_session" {
		return h.handleSessionCompletion(ctx, instanceID, nodeID, nodeExec, messages, fullResponse, toolUse.Input)
	}

	// Normal response: append assistant message and persist.
	return h.handleNormalResponse(ctx, instanceID, nodeExec, messages, fullResponse)
}

// EndSession handles user-initiated session termination. It captures existing
// output (or stores an empty object), updates instance variables, transitions
// the instance to queued, and publishes the interactive.ended event.
func (h *conversationHandler) EndSession(ctx context.Context, instanceID uuid.UUID) error {
	// 1. Load and validate instance state.
	inst, nodeID, _, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}

	// 2. Load node execution record.
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, instanceID, nodeID)
	if err != nil {
		return fmt.Errorf("conversation: load node execution: %w", err)
	}

	// 3. Determine output: use existing if present, otherwise empty object.
	output := nodeExec.Output
	if output == nil {
		output = map[string]any{}
		nodeExec.Output = output
		if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
			return fmt.Errorf("conversation: persist empty output: %w", err)
		}
	}

	// 4. Update instance variables with node output namespaced under node ID.
	vars := inst.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	vars[nodeID] = output
	if err := h.instances.UpdateVariables(ctx, instanceID, vars); err != nil {
		return fmt.Errorf("conversation: update instance variables: %w", err)
	}

	// 5. Transition to queued.
	if err := h.instances.UpdateState(ctx, instanceID, instance.StateQueued); err != nil {
		return fmt.Errorf("conversation: transition to queued: %w", err)
	}

	// 6. Publish interactive.ended event.
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data: map[string]any{
			"reason": "user_ended",
			"output": output,
		},
	})

	// 7. Notify capacity freed.
	h.queueMgr.NotifyCapacityFreed(ctx)

	return nil
}

// loadAndValidate loads an instance and verifies it is in the waiting state
// on an interactive node. It returns the instance, current node ID, and the
// interactive node's config.
func (h *conversationHandler) loadAndValidate(ctx context.Context, instanceID uuid.UUID) (*instance.Instance, string, workflow.NodeConfig, error) {
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, "", workflow.NodeConfig{}, fmt.Errorf("conversation: load instance: %w", err)
	}

	if inst.State != instance.StateWaiting {
		return nil, "", workflow.NodeConfig{}, fmt.Errorf("conversation: instance %s not in interactive wait state (state: %s)", instanceID, inst.State)
	}

	if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
		return nil, "", workflow.NodeConfig{}, fmt.Errorf("conversation: instance %s has no current node", instanceID)
	}
	nodeID := *inst.CurrentNodeID

	// Parse the workflow snapshot to get the node config.
	nodeConfig, err := findInteractiveNodeConfig(inst.WorkflowSnapshot, nodeID)
	if err != nil {
		return nil, "", workflow.NodeConfig{}, err
	}

	return inst, nodeID, nodeConfig, nil
}

// findInteractiveNodeConfig parses the workflow snapshot and returns the config
// for the specified node, verifying it is an interactive node.
func findInteractiveNodeConfig(snapshot map[string]any, nodeID string) (workflow.NodeConfig, error) {
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return workflow.NodeConfig{}, fmt.Errorf("conversation: marshal workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return workflow.NodeConfig{}, fmt.Errorf("conversation: parse workflow snapshot: %w", err)
	}

	for _, node := range wf.Nodes {
		if node.ID == nodeID {
			if node.Type != workflow.NodeTypeInteractive {
				return workflow.NodeConfig{}, fmt.Errorf("conversation: node %s is not interactive (type: %s)", nodeID, node.Type)
			}
			return node.Config, nil
		}
	}

	return workflow.NodeConfig{}, fmt.Errorf("conversation: node %s not found in workflow snapshot", nodeID)
}

// parseConversation extracts the messages array from an LLM conversation map.
func parseConversation(conv map[string]any) []map[string]any {
	if conv == nil {
		return nil
	}

	rawMessages, ok := conv["messages"]
	if !ok {
		return nil
	}

	switch msgs := rawMessages.(type) {
	case []map[string]any:
		// Make a copy to avoid mutating the original.
		result := make([]map[string]any, len(msgs))
		copy(result, msgs)
		return result
	case []any:
		result := make([]map[string]any, 0, len(msgs))
		for _, m := range msgs {
			if msgMap, ok := m.(map[string]any); ok {
				result = append(result, msgMap)
			}
		}
		return result
	default:
		return nil
	}
}

// handleSessionCompletion processes a complete_session tool call from the LLM.
func (h *conversationHandler) handleSessionCompletion(
	ctx context.Context,
	instanceID uuid.UUID,
	nodeID string,
	nodeExec *instance.NodeExecution,
	messages []map[string]any,
	assistantContent string,
	toolInput map[string]any,
) error {
	// 1. Append assistant message (with tool call content) to conversation.
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": assistantContent,
	})
	nodeExec.LLMConversation = map[string]any{"messages": messages}

	// 2. Store structured output on node execution.
	nodeExec.Output = toolInput

	// 3. Persist node execution.
	if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
		return fmt.Errorf("conversation: persist session completion: %w", err)
	}

	// 4. Update instance variables with output namespaced under node ID.
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("conversation: reload instance for variables: %w", err)
	}
	vars := inst.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	vars[nodeID] = toolInput
	if err := h.instances.UpdateVariables(ctx, instanceID, vars); err != nil {
		return fmt.Errorf("conversation: update instance variables: %w", err)
	}

	// 5. Transition to queued.
	if err := h.instances.UpdateState(ctx, instanceID, instance.StateQueued); err != nil {
		return fmt.Errorf("conversation: transition to queued: %w", err)
	}

	// 6. Publish interactive.ended event.
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data: map[string]any{
			"reason": "llm_completed",
			"output": toolInput,
		},
	})

	// 7. Notify capacity freed.
	h.queueMgr.NotifyCapacityFreed(ctx)

	return nil
}

// handleNormalResponse appends the assistant's text response to the conversation
// and persists it.
func (h *conversationHandler) handleNormalResponse(
	ctx context.Context,
	instanceID uuid.UUID,
	nodeExec *instance.NodeExecution,
	messages []map[string]any,
	fullResponse string,
) error {
	// 1. Append assistant message.
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": fullResponse,
	})
	nodeExec.LLMConversation = map[string]any{"messages": messages}

	// 2. Persist.
	if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
		return fmt.Errorf("conversation: persist assistant response: %w", err)
	}

	// 3. Publish interactive.message event for assistant.
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveMessage,
		Timestamp: time.Now(),
		Data: map[string]any{
			"role":    "assistant",
			"content": fullResponse,
		},
	})

	return nil
}
