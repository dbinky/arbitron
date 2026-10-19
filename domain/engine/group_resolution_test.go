package engine_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	groupResolverProfile = "group-resolver"
	groupResolverModel   = "resolver-model"
	groupStagingDir      = "/ws/.group-backing/run/staging/gather-1"
	groupResolverTimeout = time.Minute
)

// recordingNotifier is an engine.ResolutionNotifier that records what it was asked to raise.
type recordedNotification struct {
	instanceID   uuid.UUID
	workflowName string
	typ          notification.Type
}

type recordingNotifier struct {
	mu  sync.Mutex
	got []recordedNotification
	ids []uuid.UUID
}

var _ engine.ResolutionNotifier = (*recordingNotifier)(nil)

func (n *recordingNotifier) EmitNotification(_ context.Context, notificationID, instanceID uuid.UUID, workflowName string, t notification.Type) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, id := range n.ids {
		if id == notificationID {
			return nil
		}
	}
	n.ids = append(n.ids, notificationID)
	n.got = append(n.got, recordedNotification{instanceID: instanceID, workflowName: workflowName, typ: t})
	return nil
}

func (n *recordingNotifier) all() []recordedNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]recordedNotification(nil), n.got...)
}

// fakeNodeWaker mirrors QueueRepo.SetQueuedIfWaitingAtNode against a
// mockInstanceRepo: it re-queues ONLY an instance that is waiting AT nodeID.
type wakeCall struct {
	id     uuid.UUID
	nodeID string
}

type fakeNodeWaker struct {
	mu        sync.Mutex
	instances *mockInstanceRepo
	calls     []wakeCall
	err       error
}

var _ instance.NodeAddressedWaker = (*fakeNodeWaker)(nil)

func (w *fakeNodeWaker) SetQueuedIfWaitingAtNode(_ context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error) {
	w.mu.Lock()
	w.calls = append(w.calls, wakeCall{id: id, nodeID: nodeID})
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return false, err
	}
	w.instances.mu.Lock()
	defer w.instances.mu.Unlock()
	inst, ok := w.instances.instances[id]
	if !ok || inst.State != instance.StateWaiting || inst.CurrentNodeID == nil || *inst.CurrentNodeID != nodeID {
		return false, nil
	}
	inst.State = instance.StateQueued
	q := queuedAt
	inst.QueuedAt = &q
	return true, nil
}

func (w *fakeNodeWaker) wakeCalls() []wakeCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wakeCall(nil), w.calls...)
}

// recordingCompleter is an engine.GroupResolutionCompleter that records calls.
type completerCall struct {
	instanceID uuid.UUID
	nodeID     string
	abandoned  bool
}

type recordingCompleter struct {
	mu    sync.Mutex
	calls []completerCall
	refs  []instance.ResolutionSessionRef
	err   error
}

var _ engine.GroupResolutionCompleter = (*recordingCompleter)(nil)

func (c *recordingCompleter) OnResolutionSessionEnded(_ context.Context, ref instance.ResolutionSessionRef, abandoned bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, completerCall{instanceID: ref.InstanceID, nodeID: ref.NodeID, abandoned: abandoned})
	c.refs = append(c.refs, ref)
	return c.err
}

func (c *recordingCompleter) allRefs() []instance.ResolutionSessionRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]instance.ResolutionSessionRef(nil), c.refs...)
}

func (c *recordingCompleter) all() []completerCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]completerCall(nil), c.calls...)
}

func groupResolverResolver() *fakeResolver {
	return newFakeResolver(
		provider.Profile{Alias: groupResolverProfile, Kind: provider.ProfileKindAgent, Model: groupResolverModel, AllowedTools: []string{"Read", "Edit"}},
		provider.Profile{Alias: "chatty", Kind: provider.ProfileKindChat, Model: "chat-model"},
	)
}

func stagingHandleMap(gatherID uuid.UUID) map[string]any {
	return map[string]any{
		"snapshotHandle": "snap-1", "gatherId": gatherID.String(), "stagingDir": groupStagingDir,
		"rollbackRef": "refs/arbitron/rollback/x", "parentDir": "/ws/parent",
	}
}

type bridgeFixture struct {
	t         *testing.T
	groups    *memGroupRuns
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	agent     *mockStreamingChatProvider
	chat      *mockStreamingChatProvider
	selector  *fakeStreamSelector
	files     *ownerScopedFileReader
	notifier  *recordingNotifier
	bus       *mockEventBus
	waker     *fakeNodeWaker
	queue     *mockQueueManager
	bridge    *engine.GroupResolutionBridge
	runID     uuid.UUID
	parentID  uuid.UUID
	ownerID   uuid.UUID
	handle    engine.GatherHandle
	waitTrace uuid.UUID
}

// newBridgeFixture: a parent RUNNING at group node g1 (the gatherer runs
// inside the parent's executor), its wait trace (attempt 1), and a run in
// resolving_conflicts whose report holds attempt 1 of merge 1 (task b).
func newBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	f := &bridgeFixture{
		t: t, groups: newMemGroupRuns(), instances: newMockInstanceRepo(), nodeExecs: newMockNodeExecutionRepo(),
		agent: newMockStreamingChatProvider(), chat: newMockStreamingChatProvider(),
		files: newOwnerScopedFileReader(), notifier: &recordingNotifier{}, bus: newMockEventBus(), queue: newMockQueueManager(),
		parentID: uuid.New(), ownerID: uuid.New(),
	}
	f.selector = &fakeStreamSelector{chatProv: f.chat, agentProv: f.agent}
	f.waker = &fakeNodeWaker{instances: f.instances}
	node := "g1"
	f.instances.instances[f.parentID] = &instance.Instance{
		ID: f.parentID, OwnerID: f.ownerID, WorkflowName: "wf", State: instance.StateRunning,
		CurrentNodeID: &node, Variables: map[string]any{"workspace": "/ws/parent"},
	}
	gatherID := uuid.New()
	f.handle = engine.GatherHandle{SnapshotHandle: "snap-1", GatherID: gatherID, StagingDir: groupStagingDir, RollbackRef: "refs/arbitron/rollback/x", ParentDir: "/ws/parent"}
	run := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: f.parentID, NodeID: "g1", Attempt: 1,
		CompletionRule: workflow.GroupRuleAllSucceed, GatherMode: workflow.GroupGatherResultsAndFiles,
		Config:            map[string]any{"resolverProfile": groupResolverProfile},
		WorkspaceSnapshot: "snap-1", State: instance.GroupRunResolvingConflicts,
		Gather: &instance.GatherReport{
			Mode: workflow.GroupGatherResultsAndFiles, State: "resolving", MergeIndex: 1,
			Handle: stagingHandleMap(gatherID),
			Tasks:  []instance.GatherTaskReport{{Key: "a", Merged: true, Reason: "merged cleanly"}, {Key: "b", ConflictPaths: []string{"shared.txt"}}},
			Attempts: []instance.ResolutionAttempt{
				{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"shared.txt"}, StartedAt: time.Now().UTC()},
			},
		},
	}
	f.runID = run.ID
	f.groups.put(run)
	f.waitTrace = uuid.New()
	require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
		ID: f.waitTrace, InstanceID: f.parentID, NodeID: "g1", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"groupRunId": run.ID.String(), "state": "waiting"}, StartedAt: time.Now(),
	}))
	f.bridge = engine.NewGroupResolutionBridge(f.groups, f.instances, f.nodeExecs, groupResolverResolver(), f.selector, f.files, f.notifier, f.bus, f.waker, f.queue, groupResolverTimeout)
	return f
}

func (f *bridgeFixture) parent() *instance.Instance {
	f.t.Helper()
	p, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(f.t, err)
	return p
}

func (f *bridgeFixture) request(attempt int) engine.ResolutionRequest {
	return engine.ResolutionRequest{Handle: f.handle, MergeIndex: 1, Attempt: attempt, TaskKey: "b", TaskRef: "refs/arbitron/tasks/b", ConflictPaths: []string{"shared.txt"}}
}

func (f *bridgeFixture) start(attempt int) (*instance.ResolutionSessionRef, error) {
	return f.bridge.StartResolution(context.Background(), f.groups.snapshot(f.runID), f.parent(), f.request(attempt))
}

// sessionRows returns the parent's group_resolution rows at g1 in creation order.
func (f *bridgeFixture) sessionRows() []instance.NodeExecution {
	execs, err := f.nodeExecs.ListByInstance(context.Background(), f.parentID)
	require.NoError(f.t, err)
	var out []instance.NodeExecution
	for _, e := range execs {
		if e.NodeID == "g1" && e.LLMConversation != nil && e.LLMConversation["kind"] == engine.ConversationKindGroupResolution {
			out = append(out, e)
		}
	}
	return out
}

func (f *bridgeFixture) setActiveSession() {
	f.groups.update(f.runID, func(r *instance.GroupRun) {
		ref := f.activeRef()
		r.Gather.ActiveSession = &ref
	})
}

// activeRef is the session setActiveSession records: merge 1, attempt 1.
func (f *bridgeFixture) activeRef() instance.ResolutionSessionRef {
	return instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: f.parentID, NodeID: "g1"}
}

func (f *bridgeFixture) setParentState(s instance.State, node string) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[f.parentID].State = s
	f.instances.instances[f.parentID].CurrentNodeID = &node
}

// --- StartResolution --------------------------------------------------------

func TestGroupResolutionBridge_StartResolution_RunsAutonomousAttemptInStagingDir(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"Resolved ", "shared.txt."}, "agent-sess-1")

	ref, err := f.start(1)
	require.NoError(t, err)
	assert.Equal(t, instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: f.parentID, NodeID: "g1"}, *ref)

	reqs := f.agent.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, groupStagingDir, reqs[0].WorkDir, "the agent works in the gather's STAGING dir")
	assert.Equal(t, groupResolverModel, reqs[0].Model)
	assert.Equal(t, []string{"Read", "Edit"}, reqs[0].ToolScope)
	assert.Empty(t, reqs[0].SessionID, "the autonomous attempt starts a fresh provider session")
	assert.Contains(t, reqs[0].Prompt, "task `b`")
	assert.Contains(t, reqs[0].Prompt, "- shared.txt")
	assert.Contains(t, reqs[0].Prompt, "`MERGE_HEAD`")
	assert.Contains(t, reqs[0].Prompt, "<arbitron:result:")
	assert.Empty(t, f.chat.getCapturedReqs(), "the tool-suppressed chat stream is never used")
	assert.Equal(t, provider.ProfileKindAgent, f.selector.gotKind)

	rows := f.sessionRows()
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, f.parentID, row.InstanceID, "persisted on the PARENT")
	assert.Equal(t, 2, row.Attempt, "one past the group node's newest row (the wait trace)")
	assert.Equal(t, instance.NodeStatusSucceeded, row.Status)
	conv := row.LLMConversation
	assert.Equal(t, f.runID.String(), conv["groupRunId"])
	assert.EqualValues(t, 1, conv["mergeIndex"])
	assert.EqualValues(t, 1, conv["attempt"])
	assert.Equal(t, "b", conv["taskKey"])
	assert.Equal(t, "agent-sess-1", conv["sessionId"])
	nonce, _ := conv["nonce"].(string)
	require.NotEmpty(t, nonce)
	assert.Contains(t, reqs[0].Prompt, engine.SentinelOpenTag(nonce), "the persisted nonce scopes the sentinel the agent was told to emit")
	msgs := extractMessages(t, conv)
	require.Len(t, msgs, 2)
	assert.Equal(t, "Resolved shared.txt.", msgs[1]["content"])
	ident, ok := engine.GroupResolutionIdentityOf(conv)
	require.True(t, ok)
	assert.True(t, ident.Open)

	assert.Empty(t, f.notifier.all(), "the first turn alone does not require a human")
	f.setActiveSession()
	require.NoError(t, f.bridge.NotifyResolutionRequired(context.Background(), *ref))
	require.NoError(t, f.bridge.NotifyResolutionRequired(context.Background(), *ref))
	assert.Equal(t, []recordedNotification{{instanceID: f.parentID, workflowName: "wf", typ: notification.TypeConflictResolution}}, f.notifier.all())
	conflicts := f.bus.eventsOfType(f.parentID, engine.EventTypeGroupConflict)
	require.Len(t, conflicts, 1)
	data := conflicts[0].Data.(map[string]any)
	assert.Equal(t, f.runID.String(), data["groupRunId"])
	assert.Equal(t, "g1", data["nodeId"])
	assert.Equal(t, "b", data["taskKey"])
	assert.Equal(t, 1, data["attempt"])
	assert.Len(t, f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveToken), 2)
	assert.Len(t, f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveMessage), 1)

	trace := f.nodeExecs.executions[0]
	assert.Equal(t, f.waitTrace, trace.ID)
	assert.Nil(t, trace.LLMConversation, "the wait trace is never rewritten")
}

func TestGroupResolutionBridge_StartResolution_FreshAttemptGetsANewRow(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"try"}, "s")

	_, err := f.start(1)
	require.NoError(t, err)
	_, err = f.start(2)
	require.NoError(t, err)

	rows := f.sessionRows()
	require.Len(t, rows, 2)
	assert.Equal(t, 2, rows[0].Attempt)
	assert.Equal(t, 3, rows[1].Attempt)
	assert.EqualValues(t, 2, rows[1].LLMConversation["attempt"])
}

func TestGroupResolutionBridge_StartResolution_ChatProfileFailsClosed(t *testing.T) {
	f := newBridgeFixture(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = "chatty" })

	_, err := f.start(1)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.Empty(t, f.agent.getCapturedReqs())
	assert.Empty(t, f.chat.getCapturedReqs())
	assert.Empty(t, f.sessionRows())
	assert.Empty(t, f.notifier.all())
}

func TestGroupResolutionBridge_StartResolution_ProfileErrors(t *testing.T) {
	t.Run("unknown profile", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = "nope" })
		_, err := f.start(1)
		assert.ErrorIs(t, err, provider.ErrUnknownProfile)
	})
	t.Run("no profile configured", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.groups.update(f.runID, func(r *instance.GroupRun) { delete(r.Config, "resolverProfile") })
		_, err := f.start(1)
		assert.ErrorContains(t, err, "has no resolverProfile")
	})
}

func TestGroupResolutionBridge_StartResolution_StreamFailuresPersistNothing(t *testing.T) {
	t.Run("stream error", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			ch := make(chan engine.ChatChunk, 1)
			ch <- engine.ChatChunk{Error: errors.New("claude exited 1")}
			close(ch)
			return ch, nil
		}
		_, err := f.start(1)
		assert.ErrorContains(t, err, "claude exited 1")
		assert.Empty(t, f.sessionRows())
		assert.Empty(t, f.notifier.all())
		assert.Empty(t, f.bus.eventsOfType(f.parentID, engine.EventTypeGroupConflict))
	})
	t.Run("stream closes without completion", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			ch := make(chan engine.ChatChunk, 1)
			ch <- engine.ChatChunk{Token: "partial"}
			close(ch)
			return ch, nil
		}
		_, err := f.start(1)
		assert.ErrorContains(t, err, "stream closed without completion")
		assert.Empty(t, f.sessionRows())
	})
}

func TestGroupResolutionBridge_StartResolution_OverridePromptReadUnderParentOwner(t *testing.T) {
	f := newBridgeFixture(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverPromptFile"] = "prompts/resolve.md" })
	f.files.put(f.ownerID, "wf", "prompts/resolve.md", "CUSTOM resolve {{taskKey}} in {{workDir}}")
	f.agent.streamCallFn = cannedStreamWithSession([]string{"ok"}, "s")

	_, err := f.start(1)
	require.NoError(t, err)
	reqs := f.agent.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(reqs[0].Prompt, "CUSTOM resolve b in "+groupStagingDir))
	assert.Equal(t, []string{ownerFileKey(f.ownerID, "wf", "prompts/resolve.md")}, f.files.readKeys())
}

func TestGroupResolutionBridge_StartResolution_MissingOverrideStartsNothing(t *testing.T) {
	f := newBridgeFixture(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverPromptFile"] = "prompts/missing.md" })

	_, err := f.start(1)
	assert.ErrorIs(t, err, engine.ErrGroupResolverPrompt)
	assert.Empty(t, f.agent.getCapturedReqs())
}

func TestGroupResolutionBridge_StartResolution_RejectsIncompleteRequests(t *testing.T) {
	f := newBridgeFixture(t)
	req := f.request(1)
	req.Handle.StagingDir = ""
	_, err := f.bridge.StartResolution(context.Background(), f.groups.snapshot(f.runID), f.parent(), req)
	assert.ErrorContains(t, err, "no staging directory")

	req = f.request(1)
	req.ConflictPaths = nil
	_, err = f.bridge.StartResolution(context.Background(), f.groups.snapshot(f.runID), f.parent(), req)
	assert.ErrorContains(t, err, "no conflicted paths")

	_, err = f.bridge.StartResolution(context.Background(), nil, f.parent(), f.request(1))
	assert.Error(t, err)
}

// --- OnResolutionSessionEnded (the completer) -------------------------------

func TestGroupResolutionBridge_OnEnded_WakesWaitingParentAtNode(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.activeRef(), false))

	rep := f.groups.snapshot(f.runID).Gather
	require.NotNil(t, rep.Attempts[0].EndedAt)
	assert.Equal(t, "", rep.Attempts[0].Outcome, "an ordinary end leaves the outcome to the gatherer's verification")
	assert.Equal(t, []wakeCall{{id: f.parentID, nodeID: "g1"}}, f.waker.wakeCalls())
	assert.Equal(t, instance.StateQueued, f.parent().State, "SINK: the parent row is re-queued")
	assert.Equal(t, 1, f.queue.notifyCount())
}

func TestGroupResolutionBridge_OnEnded_AbandonRecordsOutcome(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.activeRef(), true))
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, f.groups.snapshot(f.runID).Gather.Attempts[0].Outcome)
	assert.Equal(t, instance.StateQueued, f.parent().State)
}

func TestGroupResolutionBridge_OnEnded_IsIdempotent(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")
	ctx := context.Background()

	require.NoError(t, f.bridge.OnResolutionSessionEnded(ctx, f.activeRef(), false))
	first := f.groups.snapshot(f.runID).Gather.Attempts[0].EndedAt
	reports := f.groups.callCount("ReplaceGatherReport")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(ctx, f.activeRef(), true))
	assert.Equal(t, first, f.groups.snapshot(f.runID).Gather.Attempts[0].EndedAt)
	assert.Equal(t, "", f.groups.snapshot(f.runID).Gather.Attempts[0].Outcome, "a repeated (even abandoning) end cannot rewrite an ended attempt")
	assert.Equal(t, reports, f.groups.callCount("ReplaceGatherReport"))
	assert.Len(t, f.waker.wakeCalls(), 1)
}

func TestGroupResolutionBridge_OnEnded_NeverWakesACancelledParent(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateCancelled, "g1")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.activeRef(), true))
	assert.NotNil(t, f.groups.snapshot(f.runID).Gather.Attempts[0].EndedAt, "the outcome is still recorded")
	assert.Empty(t, f.waker.wakeCalls())
	assert.Equal(t, instance.StateCancelled, f.parent().State)
	assert.Zero(t, f.queue.notifyCount())
}

func TestGroupResolutionBridge_OnEnded_ParentElsewhereIsNotWoken(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "later-node")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.activeRef(), false))
	assert.Equal(t, instance.StateWaiting, f.parent().State)
	assert.Zero(t, f.queue.notifyCount())
}

func TestGroupResolutionBridge_OnEnded_NoRunOrNoActiveSessionIsANoOp(t *testing.T) {
	f := newBridgeFixture(t)
	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.activeRef(), false), "no active session")
	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: uuid.New(), NodeID: "g1"}, false), "no run")
	assert.Zero(t, f.groups.callCount("ReplaceGatherReport"))
	assert.Empty(t, f.waker.wakeCalls())
}

func TestGroupResolutionBridge_OnEnded_WakerErrorSurfaces(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")
	f.waker.err = errors.New("database is locked")

	err := f.bridge.OnResolutionSessionEnded(context.Background(), f.activeRef(), false)
	assert.ErrorContains(t, err, "database is locked")
}

// --- CloseSession -----------------------------------------------------------

func TestGroupResolutionBridge_CloseSession_MarksTheRowEndedOnce(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"ok"}, "s")
	ref, err := f.start(1)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, f.bridge.CloseSession(ctx, *ref, "cancelled"))
	row := f.sessionRows()[0]
	assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation))
	assert.Equal(t, "cancelled", row.LLMConversation["endReason"])
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	assert.Equal(t, "cancelled", ended[0].Data.(map[string]any)["reason"])

	require.NoError(t, f.bridge.CloseSession(ctx, *ref, "error"), "closing an ended session is a no-op")
	assert.Equal(t, "cancelled", f.sessionRows()[0].LLMConversation["endReason"])
	assert.Len(t, f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded), 1)

	require.NoError(t, f.bridge.CloseSession(ctx, instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 9, Attempt: 9, InstanceID: f.parentID, NodeID: "g1"}, "x"),
		"an unknown session is a no-op")
}

// --- identity helpers + Engine.EmitNotification ----------------------------

func TestGroupResolutionIdentityOf(t *testing.T) {
	id := uuid.New()
	conv := map[string]any{"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": float64(2), "attempt": float64(3), "taskKey": "b"}
	got, ok := engine.GroupResolutionIdentityOf(conv)
	require.True(t, ok)
	assert.Equal(t, engine.GroupResolutionIdentity{GroupRunID: id, MergeIndex: 2, Attempt: 3, TaskKey: "b", Open: true}, got)

	conv["endedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	got, ok = engine.GroupResolutionIdentityOf(conv)
	require.True(t, ok)
	assert.False(t, got.Open)
	assert.False(t, engine.IsOpenGroupResolutionSession(conv))

	for name, bad := range map[string]map[string]any{
		"nil":             nil,
		"ordinary":        {"nonce": "n", "messages": []any{}},
		"bad run id":      {"kind": "group_resolution", "groupRunId": "nope", "mergeIndex": 0, "attempt": 1},
		"missing attempt": {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": 0},
		"fractional":      {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": 0.5, "attempt": 1},
		"negative merge":  {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": -1, "attempt": 1},
		"zero attempt":    {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": 0, "attempt": 0},
		"nan":             {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": math.NaN(), "attempt": 1},
		"infinite":        {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": math.Inf(1), "attempt": 1},
		"overflow":        {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": 0, "attempt": math.Exp2(63)},
		"nil run":         {"kind": "group_resolution", "groupRunId": uuid.Nil.String(), "mergeIndex": 0, "attempt": 1},
	} {
		_, ok := engine.GroupResolutionIdentityOf(bad)
		assert.False(t, ok, name)
	}
}

func TestEngine_EmitNotification_CreatesAndPublishes(t *testing.T) {
	bus := newMockEventBus()
	repo := newMockNotificationRepo()
	eng := engine.NewEngine(nil, newMockInstanceRepo(), newMockNodeExecutionRepo(), newMockSnapshotRepo(), newMockUserRepo(),
		map[workflow.NodeType]engine.NodeExecutor{}, bus, newMockQueueManager(), nil, "")
	eng.SetNotifications(repo)
	id := uuid.New()

	require.NoError(t, eng.EmitNotification(context.Background(), uuid.New(), id, "wf", notification.TypeConflictResolution))

	require.Len(t, repo.created, 1)
	assert.Equal(t, notification.TypeConflictResolution, repo.created[0].Type)
	assert.Equal(t, id, repo.created[0].InstanceID)
	assert.Equal(t, "wf", repo.created[0].WorkflowName)
	assert.Len(t, bus.eventsOfType(id, "notification.created"), 1)
}

func TestEngine_EmitNotification_ErrorsDoNotPublish(t *testing.T) {
	for _, failure := range []string{"unwired", "empty ID", "storage", "already persisted"} {
		t.Run(failure, func(t *testing.T) {
			bus := newMockEventBus()
			eng := engine.NewEngine(nil, newMockInstanceRepo(), newMockNodeExecutionRepo(), newMockSnapshotRepo(), newMockUserRepo(),
				map[workflow.NodeType]engine.NodeExecutor{}, bus, newMockQueueManager(), nil, "")
			t.Cleanup(eng.Shutdown)
			repo := newMockNotificationRepo()
			notificationID, parentID := uuid.New(), uuid.New()
			if failure != "unwired" {
				eng.SetNotifications(repo)
			}
			switch failure {
			case "empty ID":
				notificationID = uuid.Nil
			case "storage":
				repo.createErr = assert.AnError
			case "already persisted":
				repo.createErr = notification.ErrAlreadyExists
			}
			err := eng.EmitNotification(context.Background(), notificationID, parentID, "wf", notification.TypeConflictResolution)
			if failure == "already persisted" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if failure == "storage" {
					require.ErrorIs(t, err, assert.AnError)
				}
			}
			assert.Empty(t, repo.getCreated())
			assert.Empty(t, bus.eventsOfType(parentID, "notification.created"))
		})
	}
}
