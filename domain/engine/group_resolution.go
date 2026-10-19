package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Group conflict-resolution session identity, carried inside the session's
// NodeExecution.LLMConversation next to the ordinary sessionId/nonce/messages
// keys. The identity (groupRunId, mergeIndex, attempt) is what distinguishes
// THIS session from the group's wait trace and from an earlier merge's (or an
// earlier attempt's) session on the same node.
const (
	ConversationKeyKind             = "kind"
	ConversationKindGroupResolution = "group_resolution"
	ConversationKeyGroupRunID       = "groupRunId"
	ConversationKeyMergeIndex       = "mergeIndex"
	ConversationKeyAttempt          = "attempt"
	ConversationKeyTaskKey          = "taskKey"
	ConversationKeyConflictPaths    = "conflictPaths"
	ConversationKeyEndedAt          = "endedAt"
	ConversationKeyEndReason        = "endReason"
)

// GroupResolutionIdentity is the decoded identity of a resolution session.
type GroupResolutionIdentity struct {
	GroupRunID uuid.UUID
	MergeIndex int
	Attempt    int
	TaskKey    string
	Open       bool // no endedAt recorded yet
}

// GroupResolutionIdentityOf decodes conv's group-resolution identity. ok is
// false for ordinary interactive conversations and malformed identities.
// Numbers may arrive as float64 (JSON-decoded from storage) or int.
func GroupResolutionIdentityOf(conv map[string]any) (GroupResolutionIdentity, bool) {
	if !isGroupResolutionConversation(conv) {
		return GroupResolutionIdentity{}, false
	}
	rawID, _ := conv[ConversationKeyGroupRunID].(string)
	runID, err := uuid.Parse(rawID)
	if err != nil || runID == uuid.Nil {
		return GroupResolutionIdentity{}, false
	}
	mergeIndex, ok := intValue(conv[ConversationKeyMergeIndex])
	if !ok || mergeIndex < 0 {
		return GroupResolutionIdentity{}, false
	}
	attempt, ok := intValue(conv[ConversationKeyAttempt])
	if !ok || attempt < 1 {
		return GroupResolutionIdentity{}, false
	}
	taskKey, _ := conv[ConversationKeyTaskKey].(string)
	_, ended := conv[ConversationKeyEndedAt]
	return GroupResolutionIdentity{GroupRunID: runID, MergeIndex: mergeIndex, Attempt: attempt, TaskKey: taskKey, Open: !ended}, true
}

// IsOpenGroupResolutionSession reports whether conv is a group conflict-
// resolution session that has not ended.
func IsOpenGroupResolutionSession(conv map[string]any) bool {
	id, ok := GroupResolutionIdentityOf(conv)
	return ok && id.Open
}

func isGroupResolutionConversation(conv map[string]any) bool {
	kind, _ := conv[ConversationKeyKind].(string)
	return kind == ConversationKindGroupResolution
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), int64(int(n)) == n
	case float64:
		// The upper bound is exclusive: float64 rounds MaxInt up to 2^63
		// (or 2^31). Check before converting an out-of-range JSON number.
		if n != math.Trunc(n) || n < float64(math.MinInt) || n >= -float64(math.MinInt) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return intValue(i)
	default:
		return 0, false
	}
}

// closeGroupSessionConversation returns a copy of conv marked ended.
func closeGroupSessionConversation(conv map[string]any, reason string, at time.Time) map[string]any {
	out := make(map[string]any, len(conv)+2)
	for k, v := range conv {
		out[k] = v
	}
	out[ConversationKeyEndedAt] = at.UTC().Format(time.RFC3339Nano)
	out[ConversationKeyEndReason] = reason
	return out
}

// GroupResolutionCompleter receives the end of a group conflict-resolution
// session from the conversation handler (end, sentinel completion, abandon).
type GroupResolutionCompleter interface {
	// ref names the session that ended (its run, merge and attempt); an end
	// for any other session than the report's active one is ignored.
	OnResolutionSessionEnded(ctx context.Context, ref instance.ResolutionSessionRef, abandoned bool) error
}

// ResolutionNotifier durably raises an operator notification. Reusing an ID
// must succeed without creating or publishing another notification.
type ResolutionNotifier interface {
	EmitNotification(ctx context.Context, notificationID, instanceID uuid.UUID, workflowName string, notifType notification.Type) error
}

// GroupSessionCloser marks a resolution session's record ended from outside a
// conversation (the reconciler, when a parent was cancelled mid-session).
type GroupSessionCloser interface {
	CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error
	NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error
}

// EmitNotification persists a notification under the supplied stable ID before
// publishing it. A storage failure remains retryable; an existing ID is already
// delivered, even if the caller crashed before saving its own acknowledgement.
func (e *Engine) EmitNotification(ctx context.Context, notificationID, instanceID uuid.UUID, workflowName string, notifType notification.Type) error {
	if e.notifications == nil {
		return errors.New("resolution notification repository is not configured")
	}
	if notificationID == uuid.Nil {
		return errors.New("resolution notification ID is required")
	}
	n := &notification.Notification{
		ID: notificationID, InstanceID: instanceID, WorkflowName: workflowName,
		Type: notifType, CreatedAt: time.Now(),
	}
	if err := e.notifications.Create(ctx, n); err != nil {
		if errors.Is(err, notification.ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("create resolution notification: %w", err)
	}
	e.publishNotification(ctx, n)
	return nil
}

// groupResolutionOutputSchema is the shape of the sentinel completion block a
// resolution agent emits when the operator confirms.
var groupResolutionOutputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary":       map[string]any{"type": "string"},
		"resolvedPaths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required": []any{"summary"},
}

// GroupResolutionBridge opens group conflict-resolution sessions and receives
// their completion. It implements GroupResolutionStarter (for the gatherer),
// GroupResolutionCompleter (for the conversation handler) and
// GroupSessionCloser (for the reconciler).
type GroupResolutionBridge struct {
	groups      instance.GroupRunRepository
	instances   instance.InstanceRepository
	nodeExecs   instance.NodeExecutionRepository
	sessions    instance.OpenSessionRecordUpdater
	resolver    provider.ProfileResolver
	selector    InteractiveStreamSelector
	files       FileReader
	notifier    ResolutionNotifier
	bus         EventBus
	waker       instance.NodeAddressedWaker
	queueMgr    QueueManager
	turnTimeout time.Duration
	now         func() time.Time
}

var (
	_ GroupResolutionStarter   = (*GroupResolutionBridge)(nil)
	_ GroupResolutionCompleter = (*GroupResolutionBridge)(nil)
	_ GroupSessionCloser       = (*GroupResolutionBridge)(nil)
)

// NewGroupResolutionBridge constructs the bridge.
func NewGroupResolutionBridge(
	groups instance.GroupRunRepository,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	resolver provider.ProfileResolver,
	selector InteractiveStreamSelector,
	files FileReader,
	notifier ResolutionNotifier,
	bus EventBus,
	waker instance.NodeAddressedWaker,
	queueMgr QueueManager,
	turnTimeout time.Duration,
) *GroupResolutionBridge {
	if groups == nil || instances == nil || nodeExecs == nil || resolver == nil || selector == nil || files == nil || notifier == nil || bus == nil || waker == nil || queueMgr == nil {
		panic("engine: group resolution requires every collaborator")
	}
	// Session records are written back by recovery while a live end may land
	// outside the gather gate: only a write guarded by "still open" is safe.
	sessions, ok := nodeExecs.(instance.OpenSessionRecordUpdater)
	if !ok {
		panic("engine: group resolution requires a NodeExecutionRepository that implements OpenSessionRecordUpdater")
	}
	if turnTimeout <= 0 {
		panic("engine: group resolution requires a positive autonomous turn timeout")
	}
	return &GroupResolutionBridge{
		groups: groups, instances: instances, nodeExecs: nodeExecs, sessions: sessions, resolver: resolver, selector: selector,
		files: files, notifier: notifier, bus: bus, waker: waker, queueMgr: queueMgr, turnTimeout: turnTimeout, now: time.Now,
	}
}

// StartResolution runs the AUTONOMOUS first turn of a resolution session in
// the gather's staging directory, persists the session on the parent at the
// group node and publishes group.conflict. P3-T6 verifies the staging index
// after this first turn: a resolved merge continues automatically; only a
// still-conflicted merge needs operator end/abandon. The sentinel alone never
// proves that file conflicts were resolved.
func (b *GroupResolutionBridge) StartResolution(ctx context.Context, run *instance.GroupRun, parent *instance.Instance, req ResolutionRequest) (*instance.ResolutionSessionRef, error) {
	if run == nil || parent == nil {
		return nil, errors.New("group resolution: run and parent instance are required")
	}
	bus := resolutionEvents{EventBus: b.bus, runID: run.ID.String(), nodeID: run.NodeID}
	if strings.TrimSpace(req.Handle.StagingDir) == "" {
		return nil, fmt.Errorf("group resolution %q: run %s: gather has no staging directory", run.NodeID, run.ID)
	}
	if len(req.ConflictPaths) == 0 {
		return nil, fmt.Errorf("group resolution %q: run %s: no conflicted paths to resolve", run.NodeID, run.ID)
	}
	// Bound the autonomous turn independently of the adapter: a provider can
	// leave its channel open (or nil) after its own timeout. Cancelling this
	// scope on every exit also releases a provider stopped at a Done chunk.
	ctx, cancel := context.WithTimeout(ctx, b.turnTimeout)
	defer cancel()
	ref := instance.ResolutionSessionRef{GroupRunID: run.ID, InstanceID: parent.ID, NodeID: run.NodeID, MergeIndex: req.MergeIndex, Attempt: req.Attempt}
	if existing, err := findGroupSessionExec(ctx, b.nodeExecs, ref); err != nil {
		return nil, fmt.Errorf("group resolution: find existing session: %w", err)
	} else if existing != nil {
		return &ref, nil
	}
	alias := groupConfigString(run.Config, "resolverProfile")
	if alias == "" {
		return nil, fmt.Errorf("group resolution: parallel_group %q has no resolverProfile", run.NodeID)
	}
	binding, err := b.resolver.Resolve(ctx, alias)
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: resolve profile %q: %w", run.NodeID, alias, err)
	}
	// SECURITY: resolution edits files, so it runs with the interactive_agent
	// posture; a chat-kind resolverProfile fails closed before any stream starts.
	if err := assertBindingKind(run.NodeID, workflow.NodeTypeInteractiveAgent, binding.Kind); err != nil {
		return nil, fmt.Errorf("group resolution %q: resolverProfile %q: %w", run.NodeID, alias, err)
	}
	streamProvider, err := b.selector.StreamProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: select stream provider: %w", run.NodeID, err)
	}
	prompt, err := LoadGroupResolverPrompt(b.files, parent.OwnerID, parent.WorkflowName,
		groupConfigString(run.Config, "resolverPromptFile"), GroupResolverPromptData{
			TaskKey: req.TaskKey,
			// The snapshot handle is a group-run id, not a revision the agent can
			// inspect, and the backing refs are not visible in the staging repo
			// (P3-T3 shares objects only). During the conflicted merge MERGE_HEAD
			// is the task commit, whose single parent is the baseline (P3-T2
			// FinalizeTask: commit-tree -p <baseline>), so MERGE_HEAD^ names the
			// common starting point in {{workDir}}.
			BaselineRef:   "MERGE_HEAD^",
			MergeRef:      "MERGE_HEAD",
			ConflictPaths: req.ConflictPaths,
			WorkDir:       req.Handle.StagingDir,
			Attempt:       req.Attempt,
		})
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: %w", run.NodeID, err)
	}

	nonce := uuid.NewString()
	userMessage := prompt + sentinelInstruction(nonce, groupResolutionOutputSchema)
	startedAt := b.now()
	stream, err := streamProvider.StreamChat(ctx, ChatRequest{
		Prompt:    userMessage,
		Model:     binding.Model,
		WorkDir:   req.Handle.StagingDir,
		ToolScope: binding.AllowedTools,
	})
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: stream call: %w", run.NodeID, err)
	}
	var response strings.Builder
	var tokensIn, tokensOut int
	var sessionID string
	var sawDone bool
	for !sawDone {
		// Prefer cancellation even when a buffered Done or EOF is also ready.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("group resolution %q: autonomous turn: %w", run.NodeID, err)
		}
		var chunk ChatChunk
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("group resolution %q: autonomous turn: %w", run.NodeID, ctx.Err())
		case next, ok := <-stream:
			if !ok {
				if err := ctx.Err(); err != nil {
					return nil, fmt.Errorf("group resolution %q: autonomous turn: %w", run.NodeID, err)
				}
				return nil, fmt.Errorf("group resolution %q: stream closed without completion", run.NodeID)
			}
			chunk = next
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("group resolution %q: stream error: %w", run.NodeID, chunk.Error)
		}
		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}
		if chunk.Token != "" {
			response.WriteString(chunk.Token)
			bus.Publish(parent.ID, Event{Type: EventTypeInteractiveToken, Timestamp: time.Now(), Data: map[string]any{"token": chunk.Token}})
		}
		if chunk.Done {
			tokensIn, tokensOut = chunk.TokensIn, chunk.TokensOut
			sawDone = true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("group resolution %q: autonomous turn: %w", run.NodeID, err)
	}
	fullResponse := response.String()

	attemptRow, err := nextNodeAttempt(ctx, b.nodeExecs, parent.ID, run.NodeID)
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: number session record: %w", run.NodeID, err)
	}
	paths := make([]any, 0, len(req.ConflictPaths))
	for _, p := range req.ConflictPaths {
		paths = append(paths, p)
	}
	conversation := map[string]any{
		ConversationKeyKind:          ConversationKindGroupResolution,
		ConversationKeyGroupRunID:    run.ID.String(),
		ConversationKeyMergeIndex:    req.MergeIndex,
		ConversationKeyAttempt:       req.Attempt,
		ConversationKeyTaskKey:       req.TaskKey,
		ConversationKeyConflictPaths: paths,
		ConversationKeySessionID:     sessionID,
		ConversationKeyNonce:         nonce,
		ConversationKeyMessages: []map[string]any{
			{"role": "user", "content": userMessage},
			{"role": "assistant", "content": fullResponse},
		},
	}
	completedAt := b.now()
	model := binding.Model
	exec := &instance.NodeExecution{
		ID:              uuid.New(),
		InstanceID:      parent.ID,
		NodeID:          run.NodeID,
		Attempt:         attemptRow,
		Status:          instance.NodeStatusSucceeded,
		LLMConversation: conversation,
		LLMModel:        &model,
		LLMTokensIn:     &tokensIn,
		LLMTokensOut:    &tokensOut,
		StartedAt:       startedAt,
		CompletedAt:     &completedAt,
	}
	// Unlike InteractiveExecutor (log-only), a failed write is fatal here: the
	// record IS the session's identity, and a session nobody can address
	// would strand the parent.
	if err := b.nodeExecs.Create(ctx, exec); err != nil {
		return nil, fmt.Errorf("group resolution %q: persist session: %w", run.NodeID, err)
	}

	publishInteractiveMessage(bus, parent.ID, "assistant", fullResponse)
	b.bus.Publish(parent.ID, Event{
		Type:      EventTypeGroupConflict,
		Timestamp: time.Now(),
		Data: map[string]any{
			"groupRunId":    run.ID.String(),
			"nodeId":        run.NodeID,
			"taskKey":       req.TaskKey,
			"mergeIndex":    req.MergeIndex,
			"attempt":       req.Attempt,
			"conflictPaths": append([]string(nil), req.ConflictPaths...),
		},
	})
	return &instance.ResolutionSessionRef{
		GroupRunID: run.ID,
		MergeIndex: req.MergeIndex,
		Attempt:    req.Attempt,
		InstanceID: parent.ID,
		NodeID:     run.NodeID,
	}, nil
}

// NotifyResolutionRequired is called only after the gatherer verifies that
// the first agent turn left conflicts and persists the session identity.
func (b *GroupResolutionBridge) NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error {
	run, err := b.groups.Get(ctx, ref.GroupRunID)
	if err != nil {
		return fmt.Errorf("load resolution notification run: %w", err)
	}
	if run == nil || run.State != instance.GroupRunResolvingConflicts || run.Gather == nil || run.Gather.ActiveSession == nil || *run.Gather.ActiveSession != ref || run.ParentInstanceID != ref.InstanceID || run.NodeID != ref.NodeID {
		return errors.New("resolution notification does not name the active session")
	}
	exec, err := findGroupSessionExec(ctx, b.nodeExecs, ref)
	if err != nil {
		return err
	}
	if exec == nil || !IsOpenGroupResolutionSession(exec.LLMConversation) {
		return errors.New("resolution notification session is not open")
	}
	if sent, _ := exec.LLMConversation["notificationSent"].(bool); sent {
		return nil
	}
	parent, err := b.instances.Get(ctx, ref.InstanceID)
	if err != nil {
		return err
	}
	// The full attempt identity survives a bridge restart or a failed session
	// marker write. The notification primary key arbitrates concurrent retries.
	notificationID := uuid.NewSHA1(ref.GroupRunID, []byte(fmt.Sprintf("conflict_resolution/%d/%d", ref.MergeIndex, ref.Attempt)))
	if err := b.notifier.EmitNotification(ctx, notificationID, parent.ID, parent.WorkflowName, notification.TypeConflictResolution); err != nil {
		return fmt.Errorf("notify resolution required: %w", err)
	}
	// Patch only the marker: a turn saved since the read (messages, provider
	// session ID) survives, and a session ended since keeps its end (the
	// marker is moot then).
	_, err = b.sessions.PatchSessionFields(ctx, exec.ID, map[string]any{"notificationSent": true})
	return err
}

// maxSessionEndRetries bounds OnResolutionSessionEnded's re-reads after a lost
// report CAS; each loss means the gatherer made progress, so a few suffice.
const maxSessionEndRetries = 8

// OnResolutionSessionEnded records the end of ref's attempt while it is the
// ACTIVE one (outcome "abandoned" when abandoned, otherwise left for the
// gatherer to verify) and
// wakes the parent ONLY if it is still waiting at this node — never a
// cancelled, terminal or already-claimed parent. An attempt that already
// ended makes the call a no-op, so repeated end/sentinel callbacks are safe;
// so does a ref of an earlier merge or attempt than the active one, which
// must never end the session the gatherer opened after it.
//
// This runs outside the gather gate, concurrently with the parent's gatherer,
// so the end is a CAS over the report it read: when the gatherer moved the
// report on meanwhile (ended the attempt itself, committed the merge, opened
// the next one) the write is refused, the report is re-read and the end is
// re-judged against it, never written over it.
func (b *GroupResolutionBridge) OnResolutionSessionEnded(ctx context.Context, ref instance.ResolutionSessionRef, abandoned bool) error {
	instanceID, nodeID := ref.InstanceID, ref.NodeID
	var now time.Time
	for tries := 0; ; tries++ {
		run, err := b.groups.GetByParentNode(ctx, instanceID, nodeID)
		if err != nil {
			return fmt.Errorf("group resolution: load group run for %s/%s: %w", instanceID, nodeID, err)
		}
		if run == nil || run.Gather == nil || run.Gather.ActiveSession == nil {
			return nil
		}
		active := *run.Gather.ActiveSession
		if run.ID != ref.GroupRunID || active.MergeIndex != ref.MergeIndex || active.Attempt != ref.Attempt {
			return nil
		}
		report := copyGatherReport(*run.Gather)
		att := findAttempt(&report, active.MergeIndex, active.Attempt)
		if att == nil || att.EndedAt != nil {
			return nil
		}
		now = b.now().UTC()
		att.EndedAt = &now
		if abandoned {
			att.Outcome = ResolutionOutcomeAbandoned
		}
		err = b.groups.ReplaceGatherReport(ctx, run.ID, run.Gather, report)
		if err == nil {
			break
		}
		if errors.Is(err, instance.ErrGatherReportStale) && tries < maxSessionEndRetries {
			continue
		}
		return fmt.Errorf("group resolution: record session end for run %s: %w", run.ID, err)
	}
	parent, err := b.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("group resolution: load parent %s: %w", instanceID, err)
	}
	if parent.State != instance.StateWaiting {
		return nil
	}
	woke, err := b.waker.SetQueuedIfWaitingAtNode(ctx, instanceID, nodeID, now)
	if err != nil {
		return fmt.Errorf("group resolution: wake parent %s at %s: %w", instanceID, nodeID, err)
	}
	if woke && b.queueMgr != nil {
		b.queueMgr.NotifyCapacityFreed(ctx)
	}
	return nil
}

// CloseSession marks the record of ref ended with reason and publishes
// interactive.ended. Unknown or already-ended sessions — including one ended
// between this read and the write — are a no-op.
func (b *GroupResolutionBridge) CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error {
	exec, err := findGroupSessionExec(ctx, b.nodeExecs, ref)
	if err != nil {
		return fmt.Errorf("group resolution: find session %d/%d of run %s: %w", ref.MergeIndex, ref.Attempt, ref.GroupRunID, err)
	}
	if exec == nil || !IsOpenGroupResolutionSession(exec.LLMConversation) {
		return nil
	}
	exec.LLMConversation = closeGroupSessionConversation(exec.LLMConversation, reason, b.now())
	// Patch only the end keys so a turn saved since the read is not reverted.
	closed, err := b.sessions.PatchSessionFields(ctx, exec.ID, map[string]any{
		ConversationKeyEndedAt:   exec.LLMConversation[ConversationKeyEndedAt],
		ConversationKeyEndReason: exec.LLMConversation[ConversationKeyEndReason],
	})
	if err != nil {
		return fmt.Errorf("group resolution: close session %d/%d of run %s: %w", ref.MergeIndex, ref.Attempt, ref.GroupRunID, err)
	}
	if !closed {
		return nil // ended concurrently (operator end/abandon): the first end and its reason stand
	}
	conversationEvents(b.bus, exec.LLMConversation, ref.NodeID).Publish(ref.InstanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data:      map[string]any{"reason": reason, "sessionKind": ConversationKindGroupResolution},
	})
	return nil
}

// latestNodeExecution returns the newest record for (instanceID, nodeID): the
// highest Attempt, ties going to the later record in repository order (the
// SQLite repository lists by started_at). (nil, nil) when there is none.
func latestNodeExecution(ctx context.Context, repo instance.NodeExecutionRepository, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	execs, err := repo.ListByInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	var best *instance.NodeExecution
	for i := range execs {
		if execs[i].NodeID != nodeID {
			continue
		}
		if best == nil || execs[i].Attempt >= best.Attempt {
			cp := execs[i]
			best = &cp
		}
	}
	return best, nil
}

// nextNodeAttempt numbers a new record one past the node's newest record, so
// GetByInstanceAndNode (ORDER BY attempt DESC LIMIT 1) returns it.
func nextNodeAttempt(ctx context.Context, repo instance.NodeExecutionRepository, instanceID uuid.UUID, nodeID string) (int, error) {
	latest, err := latestNodeExecution(ctx, repo, instanceID, nodeID)
	if err != nil {
		return 0, err
	}
	if latest == nil {
		return 1, nil
	}
	return latest.Attempt + 1, nil
}

// findGroupSessionExec returns the session record whose identity equals ref.
func findGroupSessionExec(ctx context.Context, repo instance.NodeExecutionRepository, ref instance.ResolutionSessionRef) (*instance.NodeExecution, error) {
	execs, err := repo.ListByInstance(ctx, ref.InstanceID)
	if err != nil {
		return nil, err
	}
	for i := range execs {
		if execs[i].NodeID != ref.NodeID {
			continue
		}
		id, ok := GroupResolutionIdentityOf(execs[i].LLMConversation)
		if ok && id.GroupRunID == ref.GroupRunID && id.MergeIndex == ref.MergeIndex && id.Attempt == ref.Attempt {
			cp := execs[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func groupConfigString(cfg map[string]any, key string) string {
	s, _ := cfg[key].(string)
	return strings.TrimSpace(s)
}
