package engine

import (
	"context"
	"fmt"
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
	bindings   TurnBindingRestorer
	instances  instance.InstanceRepository
	nodeExecs  instance.NodeExecutionRepository
	selector   InteractiveStreamSelector
	resolver   provider.ProfileResolver
	fileReader FileReader
	eventBus   EventBus
	queueMgr   QueueManager
	leases     TurnLeaseStore
	leaseTTL   time.Duration
	intents    TurnIntentStore
}

// SetTurnBindingRestorer installs the accepted policy restorer for replay and recovery.
func (e *InteractiveExecutor) SetTurnBindingRestorer(r TurnBindingRestorer) {
	if r == nil {
		panic("engine: interactive SetTurnBindingRestorer requires a restorer")
	}
	e.bindings = r
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
		leaseTTL:   DefaultTurnLeaseTTL,
	}
}

// SetTurnLeases installs the durable session guard before execution.
func (e *InteractiveExecutor) SetTurnLeases(store TurnLeaseStore, ttl time.Duration) {
	if store == nil || ttl <= 0 {
		panic("engine: interactive executor SetTurnLeases requires a store and a positive TTL")
	}
	e.leases, e.leaseTTL = store, ttl
}

func (e *InteractiveExecutor) SetTurnIntents(store TurnIntentStore) {
	if store == nil {
		panic("engine: interactive executor SetTurnIntents requires a store")
	}
	e.intents = store
}

func (e *InteractiveExecutor) pendingFirstIntent(ctx context.Context, key, nodeID string) (*TurnIntent, error) {
	if e.intents == nil {
		return nil, nil
	}
	in, err := e.intents.PendingFor(ctx, key)
	if err != nil {
		return nil, err
	}
	id, _ := StepIdentityFromContext(ctx)
	if in == nil || in.Kind != TurnIntentFirst || in.NodeID != nodeID || in.InvocationID != id.InvocationID {
		return nil, nil
	}
	return in, nil
}

// Execute reads the orchestration and template files, resolves variable
// substitutions, streams the first LLM response, writes the node execution
// record, transitions the instance to waiting, and returns a wait result.
func (e *InteractiveExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (_ *NodeResult, retErr error) {
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
		existing.Status == instance.NodeStatusSucceeded &&
		!isGroupResolutionConversation(existing.LLMConversation) {
		return &NodeResult{Output: existing.Output}, nil
	}

	ctx, hold, err := holdTurnLease(ctx, e.leases, RunLeaseKey(instanceID), e.leaseTTL)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: %w", node.ID, err)
	}
	defer hold.Release()

	startTime := time.Now()

	reuse, err := e.pendingFirstIntent(ctx, RunLeaseKey(instanceID), node.ID)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: read turn intent: %w", node.ID, err)
	}
	var binding provider.ResolvedBinding
	var intent TurnIntent
	if reuse != nil {
		intent = *reuse
		if e.bindings != nil {
			binding, err = e.bindings.RestoreTurnBinding(ctx, reuse.Binding)
		} else {
			binding, err = e.resolver.Resolve(ctx, node.Config.ModelProfile)
			binding.Model, binding.AllowedTools = reuse.Model, reuse.Tools
		}
		if err != nil {
			return nil, fmt.Errorf("interactive %q: restore turn binding: %w", node.ID, err)
		}
	} else {
		binding, err = e.resolver.Resolve(ctx, node.Config.ModelProfile)
		if err != nil {
			return nil, fmt.Errorf("interactive %q: resolve profile: %w", node.ID, err)
		}
		if err := assertBindingKind(node.ID, node.Type, binding.Kind); err != nil {
			return nil, fmt.Errorf("interactive %q: %w", node.ID, err)
		}
		accepted, err := CaptureAcceptedTurnBinding(binding)
		if err != nil {
			return nil, fmt.Errorf("interactive %q: accept binding: %w", node.ID, err)
		}
		orchText, err := loadTemplate(e.fileReader, ownerFromContext(ctx), workflowName, node.Config.OrchestrationFile)
		if err != nil {
			return nil, fmt.Errorf("interactive %q: read orchestration: %w", node.ID, err)
		}
		templateText, err := loadTemplate(e.fileReader, ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
		if err != nil {
			return nil, fmt.Errorf("interactive %q: read template: %w", node.ID, err)
		}
		nonce := uuid.NewString()
		intent = newTurnIntent(ctx, hold.Lease(), instanceID, node.ID, TurnIntentFirst, "node", 1, nil)
		if id, ok := StepIdentityFromContext(ctx); ok {
			intent.OperationID = id.OperationID
		}
		intent.Prompt = orchText + "\n\n" + variable.Resolve(templateText, vars) + sentinelInstruction(nonce, node.Config.OutputSchema)
		intent.Nonce, intent.SystemPrompt = nonce, node.Config.SystemPrompt
		intent.Binding, intent.Model, intent.Tools = accepted, binding.Model, binding.AllowedTools
		if e.intents != nil {
			if err := e.intents.Persist(ctx, intent); err != nil {
				return nil, fmt.Errorf("interactive %q: persist turn intent: %w", node.ID, err)
			}
		}
	}
	if err := assertBindingKind(node.ID, node.Type, binding.Kind); err != nil {
		return nil, fmt.Errorf("interactive %q: %w", node.ID, err)
	}
	ctx = ContextWithTurnIdentity(ctx, TurnIdentityFromIntent(intent))
	defer func() {
		if e.intents != nil && (retErr == nil || (ctx.Err() == nil && !recoverableTurnError(retErr))) {
			_ = e.intents.Complete(context.WithoutCancel(ctx), intent.ID)
		}
	}()
	userMessage, nonce := intent.Prompt, intent.Nonce
	workDirStr, _ := WorkDirFromVars(vars)

	streamProvider, err := e.selector.StreamProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("interactive %q: select stream provider: %w", node.ID, err)
	}

	req := ChatRequest{
		SystemPrompt:         intent.SystemPrompt,
		Prompt:               userMessage,
		Model:                binding.Model,
		RequireExecutedModel: binding.RequiresModelEvidence(),
		MaxTokens:            node.Config.MaxTokens,
		Temperature:          node.Config.Temperature,
		WorkDir:              workDirStr,
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
	if stream == nil {
		return nil, fmt.Errorf("interactive %q: stream closed without completion (nil stream)", node.ID)
	}

	// 8. Consume the stream channel.
	var responseBuilder strings.Builder
	var tokensIn, tokensOut int
	var sessionID string
	var sawDone bool

	for {
		var chunk ChatChunk
		var open bool
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("interactive %q: stream cancelled: %w", node.ID, ctx.Err())
		case chunk, open = <-stream:
		}
		// A buffered chunk or channel closure can win the select even after
		// cancellation. Never treat that as a successfully completed turn.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("interactive %q: stream cancelled: %w", node.ID, err)
		}
		if !open {
			break
		}
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
	// Publishing the last token can itself race cancellation, including when
	// that same chunk carries Done. Check again before persisting success.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("interactive %q: stream cancelled: %w", node.ID, err)
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
	if sink, ok := PlacementSinkFromContext(ctx); ok {
		applyPlacement(nodeExec, sink.Facts())
	}
	if err := e.nodeExecs.Create(ctx, nodeExec); err != nil {
		return nil, fmt.Errorf("interactive %q: persist initial conversation: %w", node.ID, err)
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
