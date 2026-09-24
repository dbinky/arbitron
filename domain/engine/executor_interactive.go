package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
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
// Profile-aware: the executor resolves the node's model_profile to a binding,
// then picks the streaming backend via the InteractiveStreamSelector (chat
// profile -> tool-suppressed chat stream; agent profile -> tool-enabled stream).
type InteractiveExecutor struct {
	instances  instance.InstanceRepository
	nodeExecs  instance.NodeExecutionRepository
	selector   InteractiveStreamSelector
	resolver   provider.ProfileResolver
	fileReader FileReader
	eventBus   EventBus
	queueMgr   QueueManager
}

// NewInteractiveExecutor constructs a new InteractiveExecutor with all required dependencies.
func NewInteractiveExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	selector InteractiveStreamSelector,
	resolver provider.ProfileResolver,
	fileReader FileReader,
	eventBus EventBus,
	queueMgr QueueManager,
) *InteractiveExecutor {
	return &InteractiveExecutor{
		instances:  instances,
		nodeExecs:  nodeExecs,
		selector:   selector,
		resolver:   resolver,
		fileReader: fileReader,
		eventBus:   eventBus,
		queueMgr:   queueMgr,
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

	// 3. Read the orchestration file. It is composed into the same prompt as the
	// template, so it goes through loadTemplate too: an include in it must
	// expand (and be budgeted) exactly like one in the template.
	orchText, err := loadTemplate(e.fileReader, ownerFromContext(ctx), workflowName, node.Config.OrchestrationFile)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: read orchestration: %w", node.ID, err)
	}

	// 4. Read the template file.
	templateText, err := loadTemplate(e.fileReader, ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: read template: %w", node.ID, err)
	}

	// 5. Resolve {{variable}} substitutions in the template content.
	resolvedTemplate := variable.Resolve(templateText, vars)

	// 6. Compose the LLM request. The completion-sentinel instruction is
	// appended to the prompt instead of injecting a complete_session tool, and
	// the working directory is read from the {{workspace}} variable.
	//
	// SECURITY: generate a per-instance unguessable sentinel nonce. This
	// scopes sentinel parsing so an attacker cannot stage a file whose
	// contents look like a completed session and have the agent echo it
	// back. PR #8 security HIGH thread 3291943275.
	nonce := uuid.NewString()
	workDirStr, _ := WorkDirFromVars(vars)

	userMessage := orchText + "\n\n" + resolvedTemplate +
		sentinelInstruction(nonce, node.Config.OutputSchema)

	// 6a. Resolve the node's model_profile to a binding, then select the
	// streaming backend for it: a chat binding yields the tool-suppressed chat
	// stream, an agent binding yields the tool-enabled (YOLO) stream. The model
	// comes from the resolved binding, never from node.Config.Model.
	binding, err := e.resolver.Resolve(ctx, node.Config.ModelProfile)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: resolve profile: %w", node.ID, err)
	}

	// 6b. FAIL CLOSED on a binding.Kind <-> node.Type mismatch BEFORE selecting
	// a stream provider, so no tool-enabled stream can start on a chat node (or
	// vice versa). This is the tool-posture escalation boundary — see
	// ErrBindingKindMismatch. The guard does not depend on workflow validation
	// having run upstream (a snapshot or programmatic node can bypass it).
	if err := assertBindingKind(node.ID, node.Type, binding.Kind); err != nil {
		return nil, fmt.Errorf("interactive %q: %w", node.ID, err)
	}

	streamProvider, err := e.selector.StreamProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: select stream provider: %w", node.ID, err)
	}

	req := ChatRequest{
		SystemPrompt: node.Config.SystemPrompt,
		Prompt:       userMessage,
		Model:        binding.Model,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		WorkDir:      workDirStr,
		// Forward the resolved profile's per-request tool scope (PR #18 item 1b).
		// For an agent binding this bounds the tool-enabled stream to the
		// profile's explicit minimal allowlist; a chat binding ignores it.
		ToolScope: binding.AllowedTools,
	}

	// 7. Call the LLM provider with streaming.
	stream, err := streamProvider.StreamChat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: stream call: %w", node.ID, err)
	}

	// 8. Consume the stream channel.
	var responseBuilder strings.Builder
	var tokensIn, tokensOut int
	var sessionID string
	var sawDone bool

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
			sawDone = true
			break
		}
	}

	// DATA INTEGRITY (PR #18, comment 4614891622): if the stream channel closed
	// without ever delivering a terminal Done chunk (and without an error chunk),
	// the turn was cancelled or truncated mid-stream — makeSendChunk drops the
	// Done chunk on a mid-stream context cancellation (instance cancel, shutdown,
	// timeout). Recording that as NodeStatusSucceeded and advancing the
	// conversation on partial data is a silent data-integrity bug. Fail closed:
	// return an error so the node is NOT persisted as a completed success.
	if !sawDone {
		return nil, fmt.Errorf("interactive %q: stream closed without completion", node.ID)
	}

	fullResponse := responseBuilder.String()

	// 9. Build the initial conversation array. The nonce is persisted so
	// resumed turns (ConversationHandler.HandleMessage) can scope their
	// sentinel parsing to the same per-instance value.
	//
	// messages[0] is machine scaffolding (orchestration+template prompt):
	// persisted for rehydration/resumed-turn replay, intentionally not
	// published live. The live interactive.message stream is a human-chat view
	// that must not surface the prompt scaffolding (which also embeds the
	// sentinel nonce); the reconnect/history view rehydrates the full
	// conversation because resumed turns load exactly this record. This
	// asymmetry is load-bearing — do not "fix" it by publishing messages[0].
	conversation := map[string]any{
		ConversationKeySessionID: sessionID,
		ConversationKeyNonce:     nonce,
		ConversationKeyMessages: []map[string]any{
			{"role": "user", "content": userMessage},
			{"role": "assistant", "content": fullResponse},
		},
	}

	// 10. Write node execution record with succeeded status.
	now := time.Now()
	model := binding.Model
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

	// 11a. Publish a terminating interactive.message for the assistant response,
	// AFTER the conversation is persisted (step 10) and the instance is in the
	// waiting state (step 11). This mirrors ConversationHandler.handleNormalResponse
	// (persist-then-publish) and lets a client that watched the token stream reset
	// its streaming flag. Publishing only post-transition means the turn-complete
	// event is emitted once the instance is durable and actually accepting input —
	// a live subscriber is never told the turn finished before it is persisted.
	publishInteractiveMessage(e.eventBus, instanceID, "assistant", fullResponse)

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
