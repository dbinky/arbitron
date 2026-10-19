package engine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupResolutionSetter is the composition-root seam (concrete type only).
type groupResolutionSetter interface {
	SetGroupResolution(instance.GroupRunRepository, engine.GroupResolutionCompleter)
}

type groupConvoFixture struct {
	t          *testing.T
	instances  *mockInstanceRepo
	nodeExecs  *mockNodeExecutionRepo
	groups     *memGroupRuns
	agent      *mockStreamingChatProvider
	chat       *mockStreamingChatProvider
	bus        *mockEventBus
	queue      *mockQueueManager
	completer  *recordingCompleter
	handler    engine.ConversationHandler
	parentID   uuid.UUID
	runID      uuid.UUID
	waitTrace  uuid.UUID
	sessionRow uuid.UUID
}

func groupWorkflowSnapshot(t *testing.T) map[string]any {
	t.Helper()
	raw := `{
	  "name": "wf",
	  "nodes": [
	    {"id": "g1", "type": "parallel_group", "config": {"group": {
	      "form": "branches",
	      "tasks": [{"key": "a", "entry": "a1"}, {"key": "b", "entry": "b1"}],
	      "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining",
	      "gatherMode": "results_and_files", "resolverProfile": "group-resolver"}}},
	    {"id": "a1", "type": "transform", "config": {}},
	    {"id": "b1", "type": "transform", "config": {}}
	  ],
	  "edges": []
	}`
	var snap map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &snap))
	return snap
}

// newGroupConvoFixture: a parent WAITING at group node g1, the group's wait
// trace (attempt 1) and — when withSession — the open session record of merge
// 1 / attempt 1 (attempt 2), shaped exactly as the bridge persists it after a
// JSON round trip. wire=false leaves the handler as Phase 2 built it.
func newGroupConvoFixture(t *testing.T, wire, withSession bool) *groupConvoFixture {
	t.Helper()
	f := &groupConvoFixture{
		t: t, instances: newMockInstanceRepo(), nodeExecs: newMockNodeExecutionRepo(), groups: newMemGroupRuns(),
		agent: newMockStreamingChatProvider(), chat: newMockStreamingChatProvider(), bus: newMockEventBus(),
		queue: newMockQueueManager(), completer: &recordingCompleter{}, parentID: uuid.New(),
	}
	node := "g1"
	f.instances.instances[f.parentID] = &instance.Instance{
		ID: f.parentID, OwnerID: convoTestOwner, WorkflowName: "wf", State: instance.StateWaiting,
		CurrentNodeID: &node, WorkflowSnapshot: groupWorkflowSnapshot(t),
		Variables: map[string]any{"workspace": "/ws/parent", "before": "kept"},
	}
	gatherID := uuid.New()
	run := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: f.parentID, NodeID: "g1", Attempt: 1,
		CompletionRule: workflow.GroupRuleAllSucceed, GatherMode: workflow.GroupGatherResultsAndFiles,
		Config: map[string]any{"resolverProfile": groupResolverProfile}, State: instance.GroupRunResolvingConflicts,
		Gather: &instance.GatherReport{
			Mode: workflow.GroupGatherResultsAndFiles, State: "resolving", MergeIndex: 1,
			Handle:   stagingHandleMap(gatherID),
			Attempts: []instance.ResolutionAttempt{{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"shared.txt"}, StartedAt: time.Now().UTC()}},
		},
	}
	run.Gather.ActiveSession = &instance.ResolutionSessionRef{GroupRunID: run.ID, MergeIndex: 1, Attempt: 1, InstanceID: f.parentID, NodeID: "g1"}
	f.runID = run.ID
	f.groups.put(run)

	f.waitTrace = uuid.New()
	require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
		ID: f.waitTrace, InstanceID: f.parentID, NodeID: "g1", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"groupRunId": run.ID.String(), "state": "waiting"}, StartedAt: time.Now(),
	}))
	if withSession {
		f.sessionRow = uuid.New()
		require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
			ID: f.sessionRow, InstanceID: f.parentID, NodeID: "g1", Attempt: 2, Status: instance.NodeStatusSucceeded,
			LLMConversation: map[string]any{
				"kind": "group_resolution", "groupRunId": run.ID.String(), "mergeIndex": float64(1), "attempt": float64(1),
				"taskKey": "b", "conflictPaths": []any{"shared.txt"}, "sessionId": "agent-sess-1", "nonce": testNonce,
				"messages": []any{
					map[string]any{"role": "user", "content": "resolve prompt"},
					map[string]any{"role": "assistant", "content": "I merged both sides."},
				},
			},
			StartedAt: time.Now(),
		}))
	}
	f.handler = engine.NewConversationHandler(f.instances, f.nodeExecs,
		&fakeStreamSelector{chatProv: f.chat, agentProv: f.agent}, groupResolverResolver(), f.bus, f.queue)
	if wire {
		f.handler.(groupResolutionSetter).SetGroupResolution(f.groups, f.completer)
	}
	return f
}

func (f *groupConvoFixture) exec(id uuid.UUID) instance.NodeExecution {
	f.t.Helper()
	f.nodeExecs.mu.Lock()
	defer f.nodeExecs.mu.Unlock()
	for _, e := range f.nodeExecs.executions {
		if e.ID == id {
			return *e
		}
	}
	f.t.Fatalf("no node execution %s", id)
	return instance.NodeExecution{}
}

func (f *groupConvoFixture) parent() *instance.Instance {
	f.t.Helper()
	p, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(f.t, err)
	return p
}

// --- Happy path -------------------------------------------------------------

func TestConversationHandler_Group_HandleMessageStreamsIntoSessionInStagingDir(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"Fixed ", "it."}, "agent-sess-2")

	require.NoError(t, f.handler.HandleMessage(convoCtx(), f.parentID, "please keep both sides"))

	reqs := f.agent.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, groupStagingDir, reqs[0].WorkDir, "the turn runs in the persisted STAGING dir, not {{workspace}}")
	assert.Equal(t, "agent-sess-1", reqs[0].SessionID, "the session resumes the autonomous attempt's provider session")
	assert.Equal(t, groupResolverModel, reqs[0].Model)
	assert.Equal(t, []string{"Read", "Edit"}, reqs[0].ToolScope)
	assert.Equal(t, "please keep both sides", reqs[0].Prompt)
	assert.Empty(t, f.chat.getCapturedReqs())

	row := f.exec(f.sessionRow)
	msgs := extractMessages(t, row.LLMConversation)
	require.Len(t, msgs, 4)
	assert.Equal(t, "please keep both sides", msgs[2]["content"])
	assert.Equal(t, "Fixed it.", msgs[3]["content"])
	assert.Equal(t, "agent-sess-2", row.LLMConversation["sessionId"])
	ident, ok := engine.GroupResolutionIdentityOf(row.LLMConversation)
	require.True(t, ok, "the session identity survives the turn")
	assert.Equal(t, engine.GroupResolutionIdentity{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, TaskKey: "b", Open: true}, ident)

	trace := f.exec(f.waitTrace)
	assert.Nil(t, trace.LLMConversation, "the group's wait trace is never treated as the session")
	assert.Equal(t, instance.StateWaiting, f.parent().State)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_EndSessionDispatchesWithoutQueueingOrWritingVars(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	out, err := f.handler.EndSession(convoCtx(), f.parentID)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, out)

	assert.Equal(t, []completerCall{{instanceID: f.parentID, nodeID: "g1", abandoned: false}}, f.completer.all())
	p := f.parent()
	assert.Equal(t, instance.StateWaiting, p.State, "the handler never queues a group parent itself")
	_, wrote := p.Variables["g1"]
	assert.False(t, wrote, "vars[g1] is the gather's frozen output, never the session's")
	assert.Equal(t, "kept", p.Variables["before"])
	assert.Zero(t, f.queue.notifyCount())

	row := f.exec(f.sessionRow)
	assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation))
	assert.Equal(t, "user_ended", row.LLMConversation["endReason"])
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	data := ended[0].Data.(map[string]any)
	assert.Equal(t, "user_ended", data["reason"])
	assert.Equal(t, "group_resolution", data["sessionKind"])
}

func TestConversationHandler_Group_SentinelCompletionDispatchesToCompleter(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	block := engine.SentinelOpenTag(testNonce) + `{"summary":"kept both","resolvedPaths":["shared.txt"]}` + engine.SentinelCloseTag(testNonce)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"Done.\n", block}, "agent-sess-2")

	require.NoError(t, f.handler.HandleMessage(convoCtx(), f.parentID, "looks good, finish"))

	assert.Equal(t, []completerCall{{instanceID: f.parentID, nodeID: "g1", abandoned: false}}, f.completer.all())
	row := f.exec(f.sessionRow)
	assert.Equal(t, "kept both", row.Output["summary"])
	assert.Equal(t, "llm_completed", row.LLMConversation["endReason"])
	assert.Equal(t, "group_resolution", row.LLMConversation["kind"])
	_, wrote := f.parent().Variables["g1"]
	assert.False(t, wrote)
	assert.Equal(t, instance.StateWaiting, f.parent().State)
}

// --- Idempotence / identity -------------------------------------------------

func TestConversationHandler_Group_RepeatedEndIsRejected(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	require.NoError(t, err)

	_, err = f.handler.EndSession(convoCtx(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Len(t, f.completer.all(), 1, "a replayed end never reaches the completer twice")
}

func TestConversationHandler_Group_CompleterFailureLeavesSessionOpenForRetry(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.completer.err = assert.AnError

	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	require.Error(t, err)
	assert.True(t, engine.IsOpenGroupResolutionSession(f.exec(f.sessionRow).LLMConversation))

	f.completer.err = nil
	_, err = f.handler.EndSession(convoCtx(), f.parentID)
	require.NoError(t, err)
	assert.Len(t, f.completer.all(), 2)
}

func TestConversationHandler_Group_EarlierAttemptIsNotTheActiveSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.ActiveSession.Attempt = 2 })

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Empty(t, f.agent.getCapturedReqs())
}

func TestConversationHandler_Group_EarlierMergeIsNotTheActiveSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.ActiveSession.MergeIndex = 2 })

	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_RejectsMismatchedActiveSessionAddress(t *testing.T) {
	for _, field := range []string{"group", "instance", "node"} {
		t.Run(field, func(t *testing.T) {
			f := newGroupConvoFixture(t, true, true)
			f.groups.update(f.runID, func(r *instance.GroupRun) {
				switch field {
				case "group":
					r.Gather.ActiveSession.GroupRunID = uuid.New()
				case "instance":
					r.Gather.ActiveSession.InstanceID = uuid.New()
				case "node":
					r.Gather.ActiveSession.NodeID = "other"
				}
			})
			_, err := f.handler.EndSession(convoCtx(), f.parentID)
			assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
			assert.Empty(t, f.completer.all())
		})
	}
}

func TestConversationHandler_Group_WaitTraceAloneIsNotASession(t *testing.T) {
	f := newGroupConvoFixture(t, true, false)

	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_NotResolvingIsNotInteractive(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.State = instance.GroupRunGathering })

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
}

func TestConversationHandler_Group_ChatResolverProfileFailsClosed(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = "chatty" })

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.Empty(t, f.agent.getCapturedReqs())
	assert.Empty(t, f.chat.getCapturedReqs())
}

// --- Wiring / authorization -------------------------------------------------

func TestConversationHandler_Group_UnwiredHandlerKeepsGroupsNonInteractive(t *testing.T) {
	f := newGroupConvoFixture(t, false, true)

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not interactive")
	_, err = f.handler.EndSession(convoCtx(), f.parentID)
	require.Error(t, err)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_AuthorizationMatchesInteractiveNodes(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	_, err := f.handler.EndSession(context.Background(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotFound, "no identity: existence is not leaked")

	stranger := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "x", Role: identity.RoleUser})
	err = f.handler.HandleMessage(stranger, f.parentID, "hello")
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.Empty(t, f.completer.all())

	admin := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin})
	_, err = f.handler.EndSession(admin, f.parentID)
	require.NoError(t, err)
	assert.Len(t, f.completer.all(), 1)
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	assert.NotEmpty(t, ended[0].Data.(map[string]any)["actorUserId"], "a non-owner end is attributed")
}

func TestConversationHandler_Group_OrdinaryInteractiveSessionUnchanged(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	completer := &recordingCompleter{}
	h.(groupResolutionSetter).SetGroupResolution(newMemGroupRuns(), completer)

	out, err := h.EndSession(convoCtx(), instanceID)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, out)
	inst, err := instRepo.Get(context.Background(), instanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateQueued, inst.State, "interactive nodes still queue on end")
	assert.Contains(t, inst.Variables, "interact")
	assert.Empty(t, completer.all())
}
