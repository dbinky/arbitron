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

// MaxConversationTurns is the documented upper bound for the number of
// messages (user + assistant) in a single interactive session. This is not
// enforced today — it exists as a documented constant so that future rate
// limiting or auto-completion logic has a reference value. A "turn" is one
// user message plus one assistant response (2 messages). The initial
// orchestration message also counts.
const MaxConversationTurns = 50

// ConversationHandler is a domain service that processes user messages during
// an active interactive session, manages the LLM conversation loop, detects
// session completion via tool-use, and handles user-initiated session end.
type ConversationHandler interface {
	HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error
	// EndSession ends the interactive session and returns the captured output.
	// The output is captured before any state transition to avoid a race with
	// the queue manager advancing the instance.
	EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error)
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
// It loads the instance and node execution, resumes the stored claude session
// (sending only the new user message), streams the response, and detects
// session completion via the result sentinel block.
func (h *conversationHandler) HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error {
	// 1. Load and validate instance state.
	inst, nodeID, nodeConfig, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}

	// 2. Load node execution record.
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, instanceID, nodeID)
	if err != nil {
		return fmt.Errorf("conversation: load node execution: %w", err)
	}

	// 3. Parse conversation history.
	messages := parseConversation(nodeExec.LLMConversation)

	// 4. Append user message (in memory only -- not persisted until LLM succeeds).
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": content,
	})

	// 5. Publish interactive.message event for user message.
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveMessage,
		Timestamp: time.Now(),
		Data: map[string]any{
			"role":    "user",
			"content": content,
		},
	})

	// 6. Build the request: resume the stored session, send only the new
	//    user message. WorkDir comes from the instance's seeded workspace.
	workDir, _ := inst.Variables[ReservedVarWorkspace].(string)
	req := LLMRequest{
		SystemPrompt: nodeConfig.SystemPrompt,
		Prompt:       content,
		Model:        nodeConfig.Model,
		SessionID:    SessionID(nodeExec.LLMConversation),
		WorkDir:      workDir,
	}

	// 7. Call the provider with streaming.
	fullResponse, newSessionID, err := h.streamLLMResponse(ctx, instanceID, req)
	if err != nil {
		// LLM failed -- the user message was never persisted, so there is no
		// conversation corruption to roll back.
		return err
	}

	// claude returns a session id on each turn; if the stream did not carry
	// one, keep the existing id so the next turn can still resume.
	persistSessionID := newSessionID
	if persistSessionID == "" {
		persistSessionID = req.SessionID
	}

	// 8. Detect completion via the result sentinel, scoped to the per-instance
	// nonce persisted by InteractiveExecutor. If the conversation was never
	// initialised by the executor (e.g. crash recovery where the instance was
	// set to waiting before the executor wrote its record), no nonce is
	// available and sentinel completion cannot be detected on this turn — the
	// session continues as a normal response. EndSession remains the
	// operator-side escape hatch.
	var sentinelOutput map[string]any
	var completed bool
	if nonce := conversationNonce(nodeExec.LLMConversation); nonce != "" {
		var sentErr error
		sentinelOutput, completed, sentErr = extractSentinelResult(fullResponse, nonce)
		if sentErr != nil {
			return sentErr
		}
	}

	// 9. Append the assistant message.
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": fullResponse,
	})

	if completed {
		return h.handleSessionCompletion(ctx, instanceID, nodeID, nodeExec, messages, persistSessionID, sentinelOutput)
	}
	return h.handleNormalResponse(ctx, instanceID, nodeExec, messages, persistSessionID, fullResponse)
}

// EndSession handles user-initiated session termination. It captures existing
// output (or stores an empty object), updates instance variables, transitions
// the instance to queued, and publishes the interactive.ended event.
// The output is returned to the caller so it can be sent in the HTTP response
// before any state transition that might cause a race with the queue manager.
func (h *conversationHandler) EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error) {
	// 1. Load and validate instance state.
	inst, nodeID, _, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	// 2. Load node execution record.
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, instanceID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("conversation: load node execution: %w", err)
	}

	// 3. Determine output: use existing if present, otherwise empty object.
	output := nodeExec.Output
	if output == nil {
		output = map[string]any{}
		nodeExec.Output = output
		if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
			return nil, fmt.Errorf("conversation: persist empty output: %w", err)
		}
	}

	// 4. Complete the session (shared with handleSessionCompletion).
	if err := h.completeSession(ctx, inst, instanceID, nodeID, output, "user_ended"); err != nil {
		return nil, err
	}

	return output, nil
}

// completeSession performs the shared completion steps for both LLM-initiated
// (tool call) and user-initiated session termination:
//   - Update instance variables with output namespaced under node ID
//   - Transition to queued
//   - Publish interactive.ended event
//   - Notify capacity freed
func (h *conversationHandler) completeSession(
	ctx context.Context,
	inst *instance.Instance,
	instanceID uuid.UUID,
	nodeID string,
	output map[string]any,
	reason string,
) error {
	// 1. Update instance variables with output namespaced under node ID.
	vars := inst.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	vars[nodeID] = output
	if err := h.instances.UpdateVariables(ctx, instanceID, vars); err != nil {
		return fmt.Errorf("conversation: update instance variables: %w", err)
	}

	// 2. Transition to queued.
	if err := h.instances.UpdateState(ctx, instanceID, instance.StateQueued); err != nil {
		return fmt.Errorf("conversation: transition to queued: %w", err)
	}

	// 3. Publish interactive.ended event.
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data: map[string]any{
			"reason": reason,
			"output": output,
		},
	})

	// 4. Notify capacity freed.
	h.queueMgr.NotifyCapacityFreed(ctx)

	return nil
}

// streamLLMResponse calls the LLM with streaming and consumes the stream,
// publishing token events along the way. Returns the full response text and
// the provider session id (for resumption) from the terminal chunk.
func (h *conversationHandler) streamLLMResponse(
	ctx context.Context,
	instanceID uuid.UUID,
	req LLMRequest,
) (response string, sessionID string, err error) {
	stream, err := h.llmProvider.StreamCall(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("conversation: stream call: %w", err)
	}

	var responseBuilder strings.Builder
	for chunk := range stream {
		if chunk.Error != nil {
			return "", "", fmt.Errorf("conversation: stream error: %w", chunk.Error)
		}
		if chunk.Token != "" {
			responseBuilder.WriteString(chunk.Token)
			h.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": chunk.Token},
			})
		}
		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}
		if chunk.Done {
			break
		}
	}
	return responseBuilder.String(), sessionID, nil
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
		return nil, "", workflow.NodeConfig{}, fmt.Errorf("conversation: instance %s (state: %s): %w", instanceID, inst.State, instance.ErrNotInteractiveWait)
	}

	if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
		return nil, "", workflow.NodeConfig{}, fmt.Errorf("conversation: instance %s has no current node: %w", instanceID, instance.ErrNotInteractiveWait)
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
	node, err := FindNodeByID(snapshot, nodeID)
	if err != nil {
		return workflow.NodeConfig{}, fmt.Errorf("conversation: %w", err)
	}

	if node.Type != workflow.NodeTypeInteractive {
		return workflow.NodeConfig{}, fmt.Errorf("conversation: node %s is not interactive (type: %s)", nodeID, node.Type)
	}
	return node.Config, nil
}

// FindNodeByID parses a workflow snapshot and returns the node with the given ID.
// This is the shared helper that eliminates the duplicated marshal/unmarshal
// pattern across instance_handler.go, stream_handler.go, and conversation.go.
func FindNodeByID(snapshot map[string]any, nodeID string) (*workflow.Node, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("workflow snapshot is nil")
	}
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow snapshot: %w", err)
	}
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == nodeID {
			return &wf.Nodes[i], nil
		}
	}
	return nil, fmt.Errorf("node %s not found in workflow snapshot", nodeID)
}

// conversationNonce extracts the per-instance sentinel nonce persisted by
// InteractiveExecutor on the first turn. Empty when the conversation was
// created before the nonce was introduced — extractSentinelResult will refuse
// to parse an empty-nonce sentinel block, so the worst case is a session that
// cannot be completed via the sentinel and must be ended by the operator.
func conversationNonce(conv map[string]any) string {
	if conv == nil {
		return ""
	}
	n, _ := conv[ConversationKeyNonce].(string)
	return n
}

// parseConversation extracts the messages array from an LLM conversation map.
func parseConversation(conv map[string]any) []map[string]any {
	if conv == nil {
		return nil
	}

	rawMessages, ok := conv[ConversationKeyMessages]
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

// handleSessionCompletion processes a session completion signalled by the
// result sentinel block in the LLM response.
func (h *conversationHandler) handleSessionCompletion(
	ctx context.Context,
	instanceID uuid.UUID,
	nodeID string,
	nodeExec *instance.NodeExecution,
	messages []map[string]any,
	sessionID string,
	output map[string]any,
) error {
	// 1. Persist the conversation (assistant message already appended by the
	//    caller) with the refreshed session id. Carry the nonce forward so
	//    subsequent turns continue to scope sentinel parsing.
	nonce := conversationNonce(nodeExec.LLMConversation)
	nodeExec.LLMConversation = map[string]any{
		ConversationKeySessionID: sessionID,
		ConversationKeyNonce:     nonce,
		ConversationKeyMessages:  messages,
	}

	// 2. Store structured output on node execution.
	nodeExec.Output = output

	// 3. Persist node execution.
	if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
		return fmt.Errorf("conversation: persist session completion: %w", err)
	}

	// 4. Reload instance for fresh variables (avoids stale read).
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("conversation: reload instance for variables: %w", err)
	}

	// 5. Complete the session (shared steps).
	return h.completeSession(ctx, inst, instanceID, nodeID, output, "llm_completed")
}

// handleNormalResponse persists the assistant's text response to the
// conversation (already appended by the caller) with the refreshed session id.
func (h *conversationHandler) handleNormalResponse(
	ctx context.Context,
	instanceID uuid.UUID,
	nodeExec *instance.NodeExecution,
	messages []map[string]any,
	sessionID string,
	fullResponse string,
) error {
	// 1. Persist the conversation with the refreshed session id. Carry the
	//    nonce forward so subsequent turns continue to scope sentinel parsing.
	nonce := conversationNonce(nodeExec.LLMConversation)
	nodeExec.LLMConversation = map[string]any{
		ConversationKeySessionID: sessionID,
		ConversationKeyNonce:     nonce,
		ConversationKeyMessages:  messages,
	}

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
