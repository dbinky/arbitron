package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
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
	// the queue manager advancing the instance. For a parallel_group
	// conflict-resolution session it means "finish and verify".
	EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error)
	// AbandonSession rolls back a parallel-group conflict-resolution gather
	// and routes failure. Ordinary interactive sessions cannot be abandoned.
	AbandonSession(ctx context.Context, instanceID uuid.UUID) error
}

// ErrNotResolutionSession distinguishes ordinary sessions from group resolution.
var ErrNotResolutionSession = errors.New("conversation: not a group conflict-resolution session")

// conversationHandler implements ConversationHandler. It is profile-aware: a
// resumed turn resolves the waiting node's model_profile to a binding and
// selects the streaming backend via the InteractiveStreamSelector (chat profile
// -> tool-suppressed chat stream; agent profile -> tool-enabled stream).
//
// It also serves parallel_group conflict-resolution sessions (P3-T8) once
// SetGroupResolution is called: the same transports, authorization, turn
// guard, sentinel protection and history persistence, addressed to the
// group's OPEN session record, with completion dispatched to a
// GroupResolutionCompleter instead of writing node variables.
type conversationHandler struct {
	instances     instance.InstanceRepository
	nodeExecs     instance.NodeExecutionRepository
	selector      InteractiveStreamSelector
	resolver      provider.ProfileResolver
	eventBus      EventBus
	queueMgr      QueueManager
	interventions instance.InterventionRepository // optional; nil-safe attribution
	audit         AuditRecorder                   // optional; nil-safe domain audit event
	pinRelease    func(uuid.UUID)                 // optional; drops the run's cached model pins when a session completes

	// groups and groupCompleter make a parallel_group's conflict-resolution
	// session reachable through the interactive surfaces. Both nil (the
	// default) => a parallel_group node is never interactive.
	groups         instance.GroupRunRepository
	groupCompleter GroupResolutionCompleter
	// groupSessions writes a group session record only while it is still
	// open: recovery may close it while a turn streams outside the gather gate.
	groupSessions instance.OpenSessionRecordUpdater

	// turnInProgress enforces at most one in-flight interactive operation per
	// instance by mutual exclusion *with rejection* — overlapping calls are
	// refused, not queued/serialized. A turn drives a single stateful provider
	// session (e.g. `claude --resume`), which claude-cli cannot run concurrently:
	// a second overlapping send queues the prompt and no-ops the session,
	// permanently wedging the node. The set holds instance IDs with an operation
	// currently executing; HandleMessage, EndSession and AbandonSession acquire it (after
	// their auth/state gate) and a concurrent caller gets ErrInteractiveTurnInProgress
	// rather than a second concurrent resume. Does NOT cover the wider
	// EndSession/completeSession variable-rewrite race — see dbinky/arbitron#38.
	// The zero value is ready to use.
	turnInProgress sync.Map // map[uuid.UUID]struct{}
	invocations    InvocationStore
	leases         TurnLeaseStore
	leaseTTL       time.Duration
}

// groupSessionTarget holds the validated session record and its staging directory.
type groupSessionTarget struct {
	exec    *instance.NodeExecution
	workDir string
}

// NewConversationHandler constructs a new ConversationHandler with all required
// dependencies.
func NewConversationHandler(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	selector InteractiveStreamSelector,
	resolver provider.ProfileResolver,
	eventBus EventBus,
	queueMgr QueueManager,
) ConversationHandler {
	return &conversationHandler{
		instances:   instances,
		nodeExecs:   nodeExecs,
		selector:    selector,
		resolver:    resolver,
		eventBus:    eventBus,
		queueMgr:    queueMgr,
		invocations: NewMemoryInvocationStore(),
		leaseTTL:    DefaultTurnLeaseTTL,
	}
}

// SetInvocationStore installs the shared node-visit store before handling turns.
func (h *conversationHandler) SetInvocationStore(s InvocationStore) {
	if s == nil {
		s = NewMemoryInvocationStore()
	}
	h.invocations = s
}

func (h *conversationHandler) InvocationStore() InvocationStore { return h.invocations }

// SetInterventions wires the intervention audit repository so admin
// participation in an interactive session is attributed. Nil-safe: when unset,
// no attribution row is written (the actor tag is still added to events).
func (h *conversationHandler) SetInterventions(repo instance.InterventionRepository) {
	h.interventions = repo
}

// SetPinCacheRelease registers the callback that drops a run's cached model
// pins once its interactive session completes (EndSession or an LLM-completed
// session), the same function the engine releases terminal runs through. It is
// only a cache eviction: pins are immutable in the repository, so a later turn
// or node reloads them. A nil callback is rejected.
func (h *conversationHandler) SetPinCacheRelease(release func(uuid.UUID)) {
	if release == nil {
		panic("engine: conversation SetPinCacheRelease requires a non-nil release func")
	}
	h.pinRelease = release
}

// SetAuditRecorder wires the Phase-2 audit service so a lead/admin steering an
// interactive session they do not own emits a domain audit event
// (instance.lead_intervention / admin.override) — the accountability trail the
// engine's mutation paths already produce. Nil-safe: when unset, no audit event
// is written. *audit.Service.Record satisfies AuditRecorder with no adapter.
func (h *conversationHandler) SetAuditRecorder(r AuditRecorder) {
	h.audit = r
}

// SetGroupResolution enables parallel_group conflict-resolution sessions:
// groups resolves the group run (identity authority, staging dir, resolver
// profile) and completer receives each session's end. Wired by the
// composition root (P3-T11) with the GroupResolutionBridge.
func (h *conversationHandler) SetGroupResolution(groups instance.GroupRunRepository, completer GroupResolutionCompleter) {
	sessions, ok := h.nodeExecs.(instance.OpenSessionRecordUpdater)
	if !ok {
		panic("engine: group resolution sessions require a NodeExecutionRepository that implements OpenSessionRecordUpdater")
	}
	h.groups = groups
	h.groupCompleter = completer
	h.groupSessions = sessions
}

// SetTurnLeases installs the durable overlap guard before serving turns.
func (h *conversationHandler) SetTurnLeases(store TurnLeaseStore, ttl time.Duration) {
	if store == nil || ttl <= 0 {
		panic("engine: conversation SetTurnLeases requires a store and a positive TTL")
	}
	h.leases, h.leaseTTL = store, ttl
}

func sessionLeaseKey(instanceID uuid.UUID, group *groupSessionTarget) string {
	if group != nil {
		if id, ok := GroupResolutionIdentityOf(group.exec.LLMConversation); ok {
			return ResolutionLeaseKey(id)
		}
	}
	return RunLeaseKey(instanceID)
}

func (h *conversationHandler) acquireTurn(ctx context.Context, instanceID uuid.UUID, group *groupSessionTarget) (context.Context, *turnLeaseHold, error) {
	if h.leases == nil {
		if _, inFlight := h.turnInProgress.LoadOrStore(instanceID, struct{}{}); inFlight {
			return ctx, nil, fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrInteractiveTurnInProgress)
		}
		return ctx, localTurnHold(sessionLeaseKey(instanceID, group), func() { h.turnInProgress.Delete(instanceID) }), nil
	}
	turnCtx, hold, err := holdTurnLease(ctx, h.leases, sessionLeaseKey(instanceID, group), h.leaseTTL)
	if err != nil {
		return ctx, nil, fmt.Errorf("conversation: instance %s: acquire turn lease: %w", instanceID, err)
	}
	return turnCtx, hold, nil
}

// persistGroupSession writes a group session record read before this
// operation only if the STORED record is still open. A session recovery (or
// any other writer) closed meanwhile keeps its end and reason, and the caller
// gets ErrNotInteractiveWait so it stops before completing or publishing.
func (h *conversationHandler) persistGroupSession(ctx context.Context, exec *instance.NodeExecution) error {
	written, err := h.groupSessions.UpdateIfSessionOpen(ctx, exec)
	if err != nil {
		return err
	}
	if !written {
		return fmt.Errorf("conversation: instance %s: parallel_group %q session was closed during this operation: %w",
			exec.InstanceID, exec.NodeID, instance.ErrNotInteractiveWait)
	}
	return nil
}

// publishInteractiveMessage publishes a single interactive.message event for a
// chat message with the given role and content. It is the one place that
// defines the wire shape of that event (type constant, the role/content keys,
// the Timestamp) so the contract lives in exactly one location rather than
// being duplicated across the executor and conversation-handler emission sites.
//
// Scope: role+content message events only. Token (interactive.token) and ended
// (interactive.ended) events have different payload shapes and are intentionally
// NOT funneled through here.
func publishInteractiveMessage(bus EventBus, instanceID uuid.UUID, role, content string) {
	bus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveMessage,
		Timestamp: time.Now(),
		Data: map[string]any{
			"role":    role,
			"content": content,
		},
	})
}

// nonOwnerActor returns the acting user's id and true when the context identity
// is present AND differs from the instance owner (i.e. an admin participating in
// another user's session). Owner self-participation and identity-less calls
// return false, so they are not attributed.
func (h *conversationHandler) nonOwnerActor(ctx context.Context, inst *instance.Instance) (uuid.UUID, bool) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.UserID == inst.OwnerID {
		return uuid.Nil, false
	}
	return id.UserID, true
}

// recordInteractiveIntervention attributes a non-owner's interactive
// participation: it writes the best-effort attribution row AND emits the domain
// audit event (instance.lead_intervention / admin.override), via the shared
// interventionRecorder the engine mutation paths also use. Routing both paths
// through one collaborator is what guarantees the interactive surface — the
// highest-trust action in the product — can never again lose its audit trail
// (PR #21). Nil-safe and non-fatal on both sinks.
func (h *conversationHandler) recordInteractiveIntervention(ctx context.Context, inst *instance.Instance, action string, detail *string) {
	rec := interventionRecorder{interventions: h.interventions, audit: h.audit}
	rec.record(ctx, inst, action, detail)
}

// HandleMessage processes a user message in an active interactive session.
// It loads the instance and node execution, resumes the stored claude session
// (sending only the new user message), streams the response, and detects
// session completion via the result sentinel block.
func (h *conversationHandler) HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error {
	// 1. Load and validate instance state. This auth/state gate runs FIRST so an
	// unauthorized caller (or a send to a non-waiting instance) gets the same
	// ErrNotFound/ErrForbidden/ErrNotInteractiveWait it always would — it must not
	// learn whether a turn is in flight (the guard below would otherwise leak that).
	inst, nodeID, nodeConfig, group, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}

	// Scope the rest of the turn to this instance so the PinningResolver applies
	// the run's trigger-time model pins when the profile is resolved below.
	ctx = ContextWithInstanceID(ctx, instanceID)

	// Acquire after authorization; reject overlapping operations across processes.
	ctx, hold, err := h.acquireTurn(ctx, instanceID, group)
	if err != nil {
		return err
	}
	defer hold.Release()

	// The preliminary read can become stale before the guard is acquired (for
	// example, EndSession may have completed). Reload authorization, state and
	// node config under the guard before any event, resolution or session write.
	inst, nodeID, nodeConfig, group, err = h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}

	// 2. Load the session record. A group conflict-resolution session lives on
	// its OWN record (identified by group run, merge and attempt), not on
	// whatever record is newest for the node.
	nodeExec, err := h.sessionExecution(ctx, instanceID, nodeID, group)
	if err != nil {
		return err
	}

	// 3. Parse conversation history.
	messages := parseConversation(nodeExec.LLMConversation)
	bus := conversationEvents(h.eventBus, nodeExec.LLMConversation, nodeID)
	inv, err := h.invocations.Begin(ctx, instanceID, nodeID)
	if err != nil {
		return fmt.Errorf("conversation: begin invocation: %w", err)
	}
	userTurns := 0
	for _, m := range messages {
		if m["role"] == "user" {
			userTurns++
		}
	}
	opID := turnOperationID(userTurns + 1)
	if group != nil {
		if gid, ok := GroupResolutionIdentityOf(group.exec.LLMConversation); ok {
			opID = resolutionOperationID(gid.GroupRunID, gid.MergeIndex, gid.Attempt) + "/" + opID
		}
	}
	ctx = ContextWithStepIdentity(ctx, StepIdentity{RunID: instanceID, NodeID: nodeID, InvocationID: inv.ID, OperationID: opID, Kind: OperationTurn, Attempt: 1})

	sink := NewPlacementSink()
	ctx = ContextWithPlacementSink(ctx, sink)

	// 4. Append user message (in memory only -- not persisted until LLM succeeds).
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": content,
	})

	// 5. Publish interactive.message event for user message, tagging the actor
	// when an admin (non-owner) is participating, and record an attribution row.
	if actor, isOther := h.nonOwnerActor(ctx, inst); isOther {
		bus.Publish(instanceID, Event{
			Type:      EventTypeInteractiveMessage,
			Timestamp: time.Now(),
			Data: map[string]any{
				"role":        "user",
				"content":     content,
				"actorUserId": actor.String(),
			},
		})
		h.recordInteractiveIntervention(ctx, inst, instance.ActionInteractiveMsg, nil)
	} else {
		publishInteractiveMessage(bus, instanceID, "user", content)
	}

	// 5a. Resolve the waiting node's model_profile to a binding and select the
	// streaming backend the same way the InteractiveExecutor does on the first
	// turn: a chat binding yields the tool-suppressed chat stream, an agent
	// binding yields the tool-enabled stream. The model comes from the binding.
	binding, err := h.resolver.Resolve(ctx, nodeConfig.ModelProfile)
	if err != nil {
		return fmt.Errorf("conversation: resolve profile: %w", err)
	}
	// A conflict-resolution session edits files in the staging area: it must
	// run on an AGENT binding, exactly like an interactive_agent node.
	if group != nil {
		if err := assertBindingKind(nodeID, workflow.NodeTypeInteractiveAgent, binding.Kind); err != nil {
			return fmt.Errorf("conversation: group resolution %q: %w", nodeID, err)
		}
	}
	streamProvider, err := h.selector.StreamProviderFor(binding)
	if err != nil {
		return fmt.Errorf("conversation: select stream provider: %w", err)
	}

	// 6. Build the request: resume the stored session, send only the new
	//    user message. WorkDir comes from the instance's seeded workspace —
	//    or, for a conflict-resolution session, the gather's STAGING dir: the
	//    agent never works in the parent's live area or a task's directory.
	// The tool scope is the LIVE binding's, so an admin tool edit reaches a
	// resumed session; a conflict-resolution session uses its agent binding too.
	workDir, _ := inst.Variables[ReservedVarWorkspace].(string)
	toolScope := binding.AllowedTools
	if group != nil {
		workDir = group.workDir
	}
	req := ChatRequest{
		SystemPrompt:         nodeConfig.SystemPrompt,
		Prompt:               content,
		Model:                binding.Model,
		RequireExecutedModel: binding.RequiresModelEvidence(),
		ToolScope:            toolScope,
		SessionID:            SessionID(nodeExec.LLMConversation),
		WorkDir:              workDir,
	}

	// 7. Call the provider with streaming.
	fullResponse, newSessionID, err := h.streamChatResponse(ctx, instanceID, streamProvider, req, bus)
	applyPlacement(nodeExec, sink.Facts())
	if err != nil {
		if len(sink.Facts()) > 0 {
			if persistErr := h.persistTurn(context.WithoutCancel(ctx), nodeExec, group); persistErr != nil {
				return persistErr
			}
		}
		// LLM failed -- the user message was never persisted, so there is no
		// conversation corruption to roll back.
		return turnLeaseErr(ctx, err)
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

	if err := turnLeaseErr(ctx, nil); err != nil {
		return err
	}
	if completed {
		return turnLeaseErr(ctx, h.handleSessionCompletion(ctx, instanceID, nodeID, nodeExec, messages, persistSessionID, sentinelOutput, group))
	}
	return turnLeaseErr(ctx, h.handleNormalResponse(ctx, instanceID, nodeExec, messages, persistSessionID, fullResponse, group))
}

// EndSession handles user-initiated session termination. It captures existing
// output (or stores an empty object), updates instance variables, transitions
// the instance to queued, and publishes the interactive.ended event.
// The output is returned to the caller so it can be sent in the HTTP response
// before any state transition that might cause a race with the queue manager.
//
// For a group conflict-resolution session, ending means "finish and verify":
// the completer records the attempt's end and wakes the parent, whose gatherer
// then verifies the staged conflicts (it is NOT an abandon).
func (h *conversationHandler) EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error) {
	// 1. Load and validate instance state (auth/state gate first).
	inst, nodeID, _, group, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	ctx, hold, err := h.acquireTurn(ctx, instanceID, group)
	if err != nil {
		return nil, err
	}
	defer hold.Release()

	// Another operation may have completed between the first read and guard
	// acquisition. Use current authorization/state and variables, so a stale
	// EndSession cannot complete the same session twice.
	inst, nodeID, _, group, err = h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	// 2. Load the session record.
	nodeExec, err := h.sessionExecution(ctx, instanceID, nodeID, group)
	if err != nil {
		return nil, err
	}

	// 3. Determine output: use existing if present, otherwise empty object.
	output := nodeExec.Output
	if output == nil {
		output = map[string]any{}
		nodeExec.Output = output
		if err := h.persistTurn(ctx, nodeExec, group); err != nil {
			return nil, fmt.Errorf("conversation: persist empty output: %w", err)
		}
	}

	// 4. Complete the session (shared with handleSessionCompletion).
	if err := h.completeSession(ctx, inst, instanceID, nodeID, output, "user_ended", group, false); err != nil {
		return nil, err
	}

	return output, nil
}

// AbandonSession closes a group resolution session with durable abandon intent.
// It shares EndSession's authorization/state gate and turn guard. The completer
// wakes the parent, whose gatherer rolls back and routes failure on re-entry.
func (h *conversationHandler) AbandonSession(ctx context.Context, instanceID uuid.UUID) error {
	inst, nodeID, _, group, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}
	if group == nil {
		return fmt.Errorf("conversation: instance %s node %s: %w", instanceID, nodeID, ErrNotResolutionSession)
	}
	ctx, hold, err := h.acquireTurn(ctx, instanceID, group)
	if err != nil {
		return err
	}
	defer hold.Release()

	output := group.exec.Output
	if output == nil {
		output = map[string]any{}
	}
	return h.completeSession(ctx, inst, instanceID, nodeID, output, "abandoned", group, true)
}

// sessionExecution returns the record the call addresses: the open group
// session record, or the node's (newest) record for an interactive node.
func (h *conversationHandler) sessionExecution(ctx context.Context, instanceID uuid.UUID, nodeID string, group *groupSessionTarget) (*instance.NodeExecution, error) {
	if group != nil {
		return group.exec, nil
	}
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, instanceID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("conversation: load node execution: %w", err)
	}
	return nodeExec, nil
}

// completeSession performs the shared completion steps for both LLM-initiated
// (tool call) and user-initiated session termination:
//   - Update instance variables with output namespaced under node ID
//   - Transition to queued
//   - Publish interactive.ended event
//   - Notify capacity freed
//
// A group conflict-resolution session (group != nil) is dispatched to
// completeGroupSession instead.
func (h *conversationHandler) completeSession(
	ctx context.Context,
	inst *instance.Instance,
	instanceID uuid.UUID,
	nodeID string,
	output map[string]any,
	reason string,
	group *groupSessionTarget,
	abandoned bool,
) error {
	if group != nil {
		return h.completeGroupSession(ctx, inst, instanceID, nodeID, output, reason, group, abandoned)
	}

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

	// 3. Publish interactive.ended event, tagging the actor when an admin
	// (non-owner) ended the session, and record an attribution row.
	endedData := map[string]any{
		"reason": reason,
		"output": output,
	}
	if actor, isOther := h.nonOwnerActor(ctx, inst); isOther {
		endedData["actorUserId"] = actor.String()
		r := reason
		h.recordInteractiveIntervention(ctx, inst, instance.ActionInteractiveEnded, &r)
	}
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data:      endedData,
	})

	// 4. The session is over: drop the run's cached pins (reloaded on demand).
	if h.pinRelease != nil {
		h.pinRelease(instanceID)
	}

	// 5. Notify capacity freed.
	h.queueMgr.NotifyCapacityFreed(ctx)

	return nil
}

// completeGroupSession ends a group conflict-resolution session. It does NOT
// write vars[nodeID] (the group's output is the gather's frozen final output)
// and does NOT transition the instance to queued: the completer records the
// attempt's outcome and wakes the parent only if it is still waiting at this
// node (never a cancelled parent). The completer runs FIRST so a failure
// leaves the session open for a retry; a repeated end then finds the attempt
// already ended (completer no-op) or the session record closed (gate rejects).
func (h *conversationHandler) completeGroupSession(
	ctx context.Context,
	inst *instance.Instance,
	instanceID uuid.UUID,
	nodeID string,
	output map[string]any,
	reason string,
	group *groupSessionTarget,
	abandoned bool,
) error {
	id, ok := GroupResolutionIdentityOf(group.exec.LLMConversation)
	if !ok {
		return fmt.Errorf("conversation: group resolution session at %s/%s has no valid identity", instanceID, nodeID)
	}
	ref := instance.ResolutionSessionRef{GroupRunID: id.GroupRunID, MergeIndex: id.MergeIndex, Attempt: id.Attempt, InstanceID: instanceID, NodeID: nodeID}
	if err := h.groupCompleter.OnResolutionSessionEnded(ctx, ref, abandoned); err != nil {
		return fmt.Errorf("conversation: record resolution session outcome: %w", err)
	}
	exec := group.exec
	exec.LLMConversation = closeGroupSessionConversation(exec.LLMConversation, reason, time.Now())
	if exec.Output == nil {
		exec.Output = output
	}
	if err := h.persistGroupSession(ctx, exec); err != nil {
		return fmt.Errorf("conversation: close group resolution session: %w", err)
	}
	endedData := map[string]any{
		"reason":      reason,
		"output":      output,
		"sessionKind": ConversationKindGroupResolution,
	}
	if actor, isOther := h.nonOwnerActor(ctx, inst); isOther {
		endedData["actorUserId"] = actor.String()
		r := reason
		h.recordInteractiveIntervention(ctx, inst, instance.ActionInteractiveEnded, &r)
	}
	conversationEvents(h.eventBus, exec.LLMConversation, nodeID).Publish(instanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data:      endedData,
	})
	return nil
}

// streamChatResponse calls the LLM with streaming and consumes the stream,
// publishing token events along the way. Returns the full response text and
// the provider session id (for resumption) from the terminal chunk.
func (h *conversationHandler) streamChatResponse(
	ctx context.Context,
	instanceID uuid.UUID,
	chatProvider ChatProvider,
	req ChatRequest,
	bus EventBus,
) (response string, sessionID string, err error) {
	stream, err := chatProvider.StreamChat(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("conversation: stream call: %w", err)
	}
	if stream == nil {
		return "", "", fmt.Errorf("conversation: stream closed without completion (nil stream)")
	}

	var responseBuilder strings.Builder
	for {
		var chunk ChatChunk
		var open bool
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("conversation: stream cancelled: %w", ctx.Err())
		case chunk, open = <-stream:
		}
		// Cancellation may race a buffered chunk or channel closure. It must
		// never promote a partial response into a completed conversation turn.
		if err := ctx.Err(); err != nil {
			return "", "", fmt.Errorf("conversation: stream cancelled: %w", err)
		}
		if !open {
			return "", "", fmt.Errorf("conversation: stream closed without completion")
		}
		if chunk.Error != nil {
			return "", "", fmt.Errorf("conversation: stream error: %w", chunk.Error)
		}
		if chunk.Token != "" {
			responseBuilder.WriteString(chunk.Token)
			bus.Publish(instanceID, Event{
				Type:      EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": chunk.Token},
			})
		}
		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}
		if chunk.Done {
			return responseBuilder.String(), sessionID, nil
		}
	}
}

// loadAndValidate loads an instance and verifies it is in the waiting state
// on an interactive node — or, once SetGroupResolution is wired, on a
// parallel_group node whose OPEN conflict-resolution session is the group's
// active attempt. It returns the instance, current node ID, the node config
// (synthesized from the group's resolverProfile for a group session) and the
// group session target (nil for an interactive node).
func (h *conversationHandler) loadAndValidate(ctx context.Context, instanceID uuid.UUID) (*instance.Instance, string, workflow.NodeConfig, *groupSessionTarget, error) {
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: load instance: %w", err)
	}

	// Authorization: participating in (writing to) an interactive session is a
	// mutate action, allowed only for the instance owner, a lead of the run's
	// department, or an admin (canIntervene). A caller who cannot even view gets
	// ErrNotFound so the interactive surface does not leak existence; a viewing
	// peer who cannot intervene gets ErrForbidden. This gate runs before the
	// state check so a non-viewer never learns the session's state. Because both
	// HandleMessage and EndSession funnel through here, the WS, REST, and MCP
	// interactive surfaces all inherit it — group sessions included.
	if !canView(ctx, inst) {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrNotFound)
	}
	if !canIntervene(ctx, inst) {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrForbidden)
	}

	if inst.State != instance.StateWaiting {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s (state: %s): %w", instanceID, inst.State, instance.ErrNotInteractiveWait)
	}

	if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s has no current node: %w", instanceID, instance.ErrNotInteractiveWait)
	}
	nodeID := *inst.CurrentNodeID

	node, err := FindNodeByID(inst.WorkflowSnapshot, nodeID)
	if err != nil {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: %w", err)
	}
	if workflow.IsInteractive(node.Type) {
		return inst, nodeID, node.Config, nil, nil
	}
	if node.Type == workflow.NodeTypeParallelGroup && h.groups != nil && h.groupCompleter != nil {
		target, cfg, err := h.openGroupSession(ctx, inst, nodeID)
		if err != nil {
			return nil, "", workflow.NodeConfig{}, nil, err
		}
		return inst, nodeID, cfg, target, nil
	}
	return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: node %s is not interactive (type: %s)", nodeID, node.Type)
}

// openGroupSession admits a parallel_group node ONLY when the group is
// resolving conflicts AND the node's newest record is an open
// group-resolution session whose identity equals the run's ActiveSession. The
// group's wait trace, an ended session, and an earlier merge's or attempt's
// session are all refused with ErrNotInteractiveWait.
func (h *conversationHandler) openGroupSession(ctx context.Context, inst *instance.Instance, nodeID string) (*groupSessionTarget, workflow.NodeConfig, error) {
	notOpen := func(why string) error {
		return fmt.Errorf("conversation: instance %s: parallel_group %q has no open conflict-resolution session (%s): %w",
			inst.ID, nodeID, why, instance.ErrNotInteractiveWait)
	}
	run, err := h.groups.GetByParentNode(ctx, inst.ID, nodeID)
	if err != nil {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: load group run: %w", err)
	}
	if run == nil {
		return nil, workflow.NodeConfig{}, notOpen("no group run")
	}
	if run.State != instance.GroupRunResolvingConflicts || run.Gather == nil || run.Gather.ActiveSession == nil {
		return nil, workflow.NodeConfig{}, notOpen("the group is not resolving conflicts")
	}
	exec, err := latestNodeExecution(ctx, h.nodeExecs, inst.ID, nodeID)
	if err != nil {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: load node execution: %w", err)
	}
	if exec == nil {
		return nil, workflow.NodeConfig{}, notOpen("no session record")
	}
	ident, ok := GroupResolutionIdentityOf(exec.LLMConversation)
	if !ok || !ident.Open {
		return nil, workflow.NodeConfig{}, notOpen("the newest record is not an open resolution session")
	}
	active := run.Gather.ActiveSession
	if active.GroupRunID != run.ID || active.InstanceID != inst.ID || active.NodeID != nodeID ||
		ident.GroupRunID != run.ID || ident.MergeIndex != active.MergeIndex || ident.Attempt != active.Attempt {
		return nil, workflow.NodeConfig{}, notOpen(fmt.Sprintf("session for merge %d attempt %d is not the active merge %d attempt %d",
			ident.MergeIndex, ident.Attempt, active.MergeIndex, active.Attempt))
	}
	handle, ok := GatherHandleFromReport(run.Gather)
	if !ok {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: group run %s has no staged gather to resolve in", run.ID)
	}
	profile := groupConfigString(run.Config, "resolverProfile")
	if profile == "" {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: parallel_group %q has no resolverProfile", nodeID)
	}
	return &groupSessionTarget{exec: exec, workDir: handle.StagingDir},
		workflow.NodeConfig{ModelProfile: profile}, nil
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

// nextConversation carries every key of prev forward — the nonce and, for a
// group conflict-resolution session, its identity — and replaces the session
// id and messages. For an ordinary interactive conversation (sessionId, nonce,
// messages only) the result has exactly the keys it always had.
func nextConversation(prev map[string]any, sessionID string, messages []map[string]any) map[string]any {
	out := make(map[string]any, len(prev)+3)
	for k, v := range prev {
		out[k] = v
	}
	out[ConversationKeySessionID] = sessionID
	out[ConversationKeyNonce] = conversationNonce(prev)
	out[ConversationKeyMessages] = messages
	return out
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
	group *groupSessionTarget,
) error {
	// 1. Persist the conversation (assistant message already appended by the
	//    caller) with the refreshed session id. Carry the nonce (and any group
	//    session identity) forward.
	nodeExec.LLMConversation = nextConversation(nodeExec.LLMConversation, sessionID, messages)

	// 2. Store structured output on node execution.
	nodeExec.Output = output

	// 3. Persist node execution. A group session closed during the stream
	//    (recovery) stays closed and is never completed by this turn.
	if err := h.persistTurn(ctx, nodeExec, group); err != nil {
		return fmt.Errorf("conversation: persist session completion: %w", err)
	}

	// 4. Reload instance for fresh variables (avoids stale read).
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("conversation: reload instance for variables: %w", err)
	}

	// 5. Complete the session (shared steps).
	return h.completeSession(ctx, inst, instanceID, nodeID, output, "llm_completed", group, false)
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
	group *groupSessionTarget,
) error {
	// 1. Persist the conversation with the refreshed session id. Carry the
	//    nonce (and any group session identity) forward so subsequent turns
	//    continue to scope sentinel parsing and address the same session.
	nodeExec.LLMConversation = nextConversation(nodeExec.LLMConversation, sessionID, messages)

	// 2. Persist (a group session closed during the stream stays closed).
	if err := h.persistTurn(ctx, nodeExec, group); err != nil {
		return fmt.Errorf("conversation: persist assistant response: %w", err)
	}

	// 3. Publish interactive.message event for assistant.
	publishInteractiveMessage(conversationEvents(h.eventBus, nodeExec.LLMConversation, nodeExec.NodeID), instanceID, "assistant", fullResponse)

	return nil
}

// persistTurn writes a turn's record: unconditionally for an interactive
// node, guarded by "still open" for a group resolution session.
func (h *conversationHandler) persistTurn(ctx context.Context, exec *instance.NodeExecution, group *groupSessionTarget) error {
	if group != nil {
		return h.persistGroupSession(ctx, exec)
	}
	return h.nodeExecs.Update(ctx, exec)
}
