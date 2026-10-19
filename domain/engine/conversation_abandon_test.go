package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Unit: the handler ------------------------------------------------------

func TestConversationHandler_AbandonSession_RecordsAbandonAndClosesSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	require.NoError(t, f.handler.AbandonSession(convoCtx(), f.parentID))

	assert.Equal(t, []completerCall{{instanceID: f.parentID, nodeID: "g1", abandoned: true}}, f.completer.all())
	row := f.exec(f.sessionRow)
	assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation))
	assert.Equal(t, "abandoned", row.LLMConversation["endReason"])
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	data := ended[0].Data.(map[string]any)
	assert.Equal(t, "abandoned", data["reason"])
	assert.Equal(t, "group_resolution", data["sessionKind"])
	p := f.parent()
	assert.Equal(t, instance.StateWaiting, p.State, "the completer, not the handler, decides whether to wake")
	_, wrote := p.Variables["g1"]
	assert.False(t, wrote)
}

func TestConversationHandler_AbandonSession_OrdinaryInteractiveSessionIsRefused(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	completer := &recordingCompleter{}
	h.(groupResolutionSetter).SetGroupResolution(newMemGroupRuns(), completer)

	err := h.AbandonSession(convoCtx(), instanceID)
	assert.ErrorIs(t, err, engine.ErrNotResolutionSession)

	inst, gerr := instRepo.Get(context.Background(), instanceID)
	require.NoError(t, gerr)
	assert.Equal(t, instance.StateWaiting, inst.State, "an ordinary session is untouched")
	assert.NotContains(t, inst.Variables, "interact")
	assert.Empty(t, eventBus.eventsOfType(instanceID, engine.EventTypeInteractiveEnded))
	assert.Empty(t, completer.all())
}

func TestConversationHandler_AbandonSession_AuthorizationMatchesEndSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	err := f.handler.AbandonSession(context.Background(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotFound, "no identity: existence is not leaked")

	stranger := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "x", Role: identity.RoleUser})
	assert.ErrorIs(t, f.handler.AbandonSession(stranger, f.parentID), instance.ErrNotFound)

	f.instances.mu.Lock()
	f.instances.instances[f.parentID].Visibility = instance.VisibilityDepartment
	f.instances.instances[f.parentID].Department = "eng"
	f.instances.mu.Unlock()
	peer := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "peer", Role: identity.RoleUser, Departments: []string{"eng"}})
	assert.ErrorIs(t, f.handler.AbandonSession(peer, f.parentID), instance.ErrForbidden, "a department peer may view but not abandon")

	assert.Empty(t, f.completer.all())
	assert.True(t, engine.IsOpenGroupResolutionSession(f.exec(f.sessionRow).LLMConversation))
}

func TestConversationHandler_AbandonSession_RepeatIsRejected(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	require.NoError(t, f.handler.AbandonSession(convoCtx(), f.parentID))

	assert.ErrorIs(t, f.handler.AbandonSession(convoCtx(), f.parentID), instance.ErrNotInteractiveWait)
	assert.Len(t, f.completer.all(), 1)
}

func TestConversationHandler_AbandonSession_TurnInProgressIsRejected(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	started := make(chan struct{})
	release := make(chan struct{})
	f.agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		close(started)
		ch := make(chan engine.ChatChunk, 2)
		go func() {
			<-release
			ch <- engine.ChatChunk{Token: "ok"}
			ch <- engine.ChatChunk{Done: true}
			close(ch)
		}()
		return ch, nil
	}
	done := make(chan error, 1)
	go func() { done <- f.handler.HandleMessage(convoCtx(), f.parentID, "keep going") }()
	<-started

	assert.ErrorIs(t, f.handler.AbandonSession(convoCtx(), f.parentID), instance.ErrInteractiveTurnInProgress)
	close(release)
	require.NoError(t, <-done)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_AbandonSession_CancelledParentIsRefused(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.instances.mu.Lock()
	f.instances.instances[f.parentID].State = instance.StateCancelled
	f.instances.mu.Unlock()

	assert.ErrorIs(t, f.handler.AbandonSession(convoCtx(), f.parentID), instance.ErrNotInteractiveWait)
	assert.Empty(t, f.completer.all())
	assert.True(t, engine.IsOpenGroupResolutionSession(f.exec(f.sessionRow).LLMConversation),
		"the cancelled parent's session is closed by the reconciler (P3-T10), not by a transport")
}

// --- Chain: real handler + bridge + gatherer --------------------------------

type abandonChain struct {
	f         *gatherFixture
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	queue     *mockQueueManager
	bridge    *engine.GroupResolutionBridge
	gatherer  *engine.FileGatherer
	handler   engine.ConversationHandler
}

// newAbandonChain wires the REAL GroupResolutionBridge and FileGatherer and a
// REAL conversation handler over shared fakes: run a/b with b conflicting.
func newAbandonChain(t *testing.T) *abandonChain {
	t.Helper()
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = groupResolverProfile })
	c := &abandonChain{f: f, instances: newMockInstanceRepo(), nodeExecs: newMockNodeExecutionRepo(), queue: newMockQueueManager()}
	agent := newMockStreamingChatProvider()
	agent.streamCallFn = cannedStreamWithSession([]string{"I could not combine shared.txt safely."}, "sess-1")
	selector := &fakeStreamSelector{chatProv: agent, agentProv: agent}
	node := "g1"
	parent := *f.parent
	parent.OwnerID = convoTestOwner
	parent.CurrentNodeID = &node
	parent.WorkflowSnapshot = groupWorkflowSnapshot(t)
	c.instances.instances[parent.ID] = &parent
	c.bridge = engine.NewGroupResolutionBridge(f.groups, c.instances, c.nodeExecs, groupResolverResolver(), selector,
		newOwnerScopedFileReader(), &recordingNotifier{}, f.bus, &fakeNodeWaker{instances: c.instances}, c.queue, groupResolverTimeout)
	c.gatherer = engine.NewGroupGatherer(f.groups, f.area, c.bridge, f.bus)
	c.handler = engine.NewConversationHandler(c.instances, c.nodeExecs, selector, groupResolverResolver(), f.bus, c.queue)
	c.handler.(groupResolutionSetter).SetGroupResolution(f.groups, c.bridge)
	return c
}

func (c *abandonChain) setParentState(s instance.State) {
	c.instances.mu.Lock()
	defer c.instances.mu.Unlock()
	c.instances.instances[c.f.parent.ID].State = s
}

func (c *abandonChain) parent(t *testing.T) *instance.Instance {
	t.Helper()
	p, err := c.instances.Get(context.Background(), c.f.parent.ID)
	require.NoError(t, err)
	return p
}

func (c *abandonChain) gather(t *testing.T) (*engine.GatherStep, error) {
	t.Helper()
	return c.gatherer.Gather(context.Background(), c.f.groups.snapshot(c.f.runID), c.parent(t))
}

func TestGroupAbandon_Chain_RollsBackAndRoutesFailure(t *testing.T) {
	c := newAbandonChain(t)
	step, err := c.gather(t)
	require.NoError(t, err)
	require.False(t, step.Done, "the conflict opened a session")
	c.setParentState(instance.StateWaiting) // what the executor does on a Done=false step (P3-T10)

	require.NoError(t, c.handler.AbandonSession(convoCtx(), c.f.parent.ID))
	assert.Equal(t, instance.StateQueued, c.parent(t).State, "abandon wakes the waiting parent")
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, c.f.groups.snapshot(c.f.runID).Gather.Attempts[0].Outcome)

	c.setParentState(instance.StateRunning) // the queue restarted the parent at the group node
	step, err = c.gather(t)
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, "rolled_back", gatherSection(t, step)["state"])
	assert.Equal(t, 1, c.f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, c.f.area.parentFiles(gatherParentDir),
		"SINK: no task change (not even a's clean merge) reached the parent's working area")

	execs, err := c.nodeExecs.ListByInstance(context.Background(), c.f.parent.ID)
	require.NoError(t, err)
	var closed int
	for _, e := range execs {
		if e.LLMConversation["kind"] == engine.ConversationKindGroupResolution {
			assert.Equal(t, "abandoned", e.LLMConversation["endReason"])
			closed++
		}
	}
	assert.Equal(t, 1, closed)
}

// Parent cancellation mid-session: every transport refuses (the session is
// aborted for operators), a stray completion callback never resurrects the
// parent, and nothing is rolled back HERE — the reconciler (P3-T10,
// TestGroupGatherReconciler_CancelledParentMidSession_RollsBackAndSettles)
// rolls back and settles while the parent stays cancelled.
func TestGroupAbandon_Chain_CancelledParentMidSessionStaysCancelled(t *testing.T) {
	c := newAbandonChain(t)
	_, err := c.gather(t)
	require.NoError(t, err)
	c.setParentState(instance.StateCancelled)

	assert.ErrorIs(t, c.handler.AbandonSession(convoCtx(), c.f.parent.ID), instance.ErrNotInteractiveWait)
	_, err = c.handler.EndSession(convoCtx(), c.f.parent.ID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.ErrorIs(t, c.handler.HandleMessage(convoCtx(), c.f.parent.ID, "hello?"), instance.ErrNotInteractiveWait)

	require.NoError(t, c.bridge.OnResolutionSessionEnded(context.Background(), *c.f.groups.snapshot(c.f.runID).Gather.ActiveSession, true))
	assert.Equal(t, instance.StateCancelled, c.parent(t).State)
	assert.Zero(t, c.queue.notifyCount())
	assert.Equal(t, instance.GroupRunResolvingConflicts, c.f.groups.snapshot(c.f.runID).State)
	assert.Zero(t, c.f.area.applyCount())
}
