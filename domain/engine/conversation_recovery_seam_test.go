package engine_test

// Seam: Area 3 conflict resolution ↔ Area 7 group recovery (paired:7), the
// live → recovery direction. A live turn reads the session record, streams
// for minutes OUTSIDE the gather gate, then writes the record back. Recovery
// (GroupGatherReconciler.RecoverRun → abort → CloseSession) may close that
// record mid-stream for a cancelled parent. The turn's write must lose: the
// record stays closed with recovery's reason, and the turn neither completes
// the attempt nor publishes as if the session were still live.

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveTurnAcrossRecovery opens a real session (real gatherer + bridge), starts
// a real handler turn whose stream blocks until recovery has run on the now
// cancelled parent, then delivers reply(nonce) and returns the turn's error.
func liveTurnAcrossRecovery(t *testing.T, reply func(nonce string) []string) (*abandonChain, error) {
	t.Helper()
	c := newAbandonChain(t)
	ctx := context.Background()
	c.f.groups.update(c.f.runID, func(r *instance.GroupRun) { r.WorkspaceSnapshot = r.ID.String() })
	for _, task := range c.f.groups.snapshot(c.f.runID).Tasks {
		require.NoError(t, c.instances.Create(ctx, &instance.Instance{ID: task.ChildInstanceID, OwnerID: c.f.parent.OwnerID, State: instance.StateCompleted}))
	}
	inventory := newFakeInventory(c.f.area)
	inventory.add(c.f.runID.String(), time.Now())
	pruner := engine.NewGroupResourcePruner(c.f.groups, c.instances, &fakeChildLister{}, newDirWorkspaces(t), c.f.area, inventory)
	rec := engine.NewGroupGatherReconciler(c.f.groups, c.instances, c.nodeExecs, c.gatherer, c.bridge, c.bridge,
		&fakeNodeWaker{instances: c.instances}, c.queue, &fakeGatherLocker{}, c.f.bus, pruner)

	step, err := c.gather(t)
	require.NoError(t, err)
	require.False(t, step.Done, "the conflict opened a session")
	c.setParentState(instance.StateWaiting)
	nonce, _ := sessionRecord(t, c)[engine.ConversationKeyNonce].(string)
	require.NotEmpty(t, nonce)

	started, release := make(chan struct{}), make(chan struct{})
	agent := newMockStreamingChatProvider()
	agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		close(started)
		ch := make(chan engine.ChatChunk, 8)
		go func() {
			<-release
			for _, tok := range reply(nonce) {
				ch <- engine.ChatChunk{Token: tok}
			}
			ch <- engine.ChatChunk{Done: true, SessionID: "sess-live"}
			close(ch)
		}()
		return ch, nil
	}
	c.handler = engine.NewConversationHandler(c.instances, c.nodeExecs, &fakeStreamSelector{chatProv: agent, agentProv: agent},
		groupResolverResolver(), c.f.bus, c.queue)
	c.handler.(groupResolutionSetter).SetGroupResolution(c.f.groups, c.bridge)

	done := make(chan error, 1)
	go func() { done <- c.handler.HandleMessage(convoCtx(), c.f.parent.ID, "keep both sides") }()
	<-started

	c.setParentState(instance.StateCancelled)
	require.NoError(t, rec.RecoverRun(ctx, c.f.runID))
	require.Equal(t, "cancelled", sessionRecord(t, c)[engine.ConversationKeyEndReason], "precondition: recovery closed the session mid-turn")
	require.Equal(t, instance.GroupRunDone, c.f.groups.snapshot(c.f.runID).State, "precondition: recovery settled the run")

	close(release)
	return c, <-done
}

func sessionRecord(t *testing.T, c *abandonChain) map[string]any {
	t.Helper()
	execs, err := c.nodeExecs.ListByInstance(context.Background(), c.f.parent.ID)
	require.NoError(t, err)
	var conv map[string]any
	for _, e := range execs {
		if e.LLMConversation[engine.ConversationKeyKind] == engine.ConversationKindGroupResolution {
			require.Nil(t, conv, "exactly one session record")
			conv = e.LLMConversation
		}
	}
	require.NotNil(t, conv)
	return conv
}

func assertRecoveryCloseStands(t *testing.T, c *abandonChain, turnErr error) {
	t.Helper()
	assert.ErrorIs(t, turnErr, instance.ErrNotInteractiveWait, "a turn that lost its session reports it")
	conv := sessionRecord(t, c)
	assert.False(t, engine.IsOpenGroupResolutionSession(conv), "the turn never reopens the session recovery closed")
	assert.Equal(t, "cancelled", conv[engine.ConversationKeyEndReason], "recovery's end reason stands")
	assert.NotEqual(t, "sess-live", conv[engine.ConversationKeySessionID], "the late turn is not persisted")
	run := c.f.groups.snapshot(c.f.runID)
	require.NotNil(t, run.Gather)
	assert.Equal(t, engine.ResolutionOutcomeCancelled, run.Gather.Attempts[0].Outcome)
	assert.Equal(t, instance.StateCancelled, c.parent(t).State)
	for _, ev := range c.f.bus.eventsOfType(c.f.parent.ID, engine.EventTypeInteractiveMessage) {
		data := ev.Data.(map[string]any)
		if data["role"] == "assistant" {
			assert.NotContains(t, data["content"], "kept both", "no late reply is published for a closed session")
			assert.NotContains(t, data["content"], "I merged both sides.", "no late reply is published for a closed session")
		}
	}
	ended := c.f.bus.eventsOfType(c.f.parent.ID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1, "only recovery's close is announced")
	assert.Equal(t, "cancelled", ended[0].Data.(map[string]any)["reason"])
}

func TestSeamResolutionRecovery_LiveNormalTurnNeverReopensARecoveryClose(t *testing.T) {
	c, err := liveTurnAcrossRecovery(t, func(string) []string { return []string{"I merged both sides."} })
	assertRecoveryCloseStands(t, c, err)
}

func TestSeamResolutionRecovery_LiveSentinelTurnNeverReplacesARecoveryClose(t *testing.T) {
	c, err := liveTurnAcrossRecovery(t, func(nonce string) []string {
		return []string{"Done.\n", engine.SentinelOpenTag(nonce) + `{"summary":"kept both","resolvedPaths":["shared.txt"]}` + engine.SentinelCloseTag(nonce)}
	})
	assertRecoveryCloseStands(t, c, err)
}

// closeOnEnd is a completer that lets recovery's close land between the
// handler's completer call and its own close write (completeGroupSession).
type closeOnEnd struct {
	closer engine.GroupSessionCloser
	calls  int
}

func (c *closeOnEnd) OnResolutionSessionEnded(ctx context.Context, ref instance.ResolutionSessionRef, _ bool) error {
	c.calls++
	return c.closer.CloseSession(ctx, ref, "cancelled")
}

func TestSeamResolutionRecovery_LiveEndNeverReplacesARecoveryCloseLandingAfterTheCompleter(t *testing.T) {
	c := newAbandonChain(t)
	step, err := c.gather(t)
	require.NoError(t, err)
	require.False(t, step.Done)
	c.setParentState(instance.StateWaiting)
	completer := &closeOnEnd{closer: c.bridge}
	c.handler.(groupResolutionSetter).SetGroupResolution(c.f.groups, completer)

	_, err = c.handler.EndSession(convoCtx(), c.f.parent.ID)

	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Equal(t, 1, completer.calls)
	conv := sessionRecord(t, c)
	assert.Equal(t, "cancelled", conv[engine.ConversationKeyEndReason], "the first close wins")
	ended := c.f.bus.eventsOfType(c.f.parent.ID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1, "a lost close publishes no second interactive.ended")
	assert.Equal(t, "cancelled", ended[0].Data.(map[string]any)["reason"])
}

func TestSeamResolutionRecovery_LiveEndNeverReopensARecoveryCloseLandingAfterTheGate(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	repo := &endAfterRead{mockNodeExecutionRepo: f.nodeExecs}
	h := engine.NewConversationHandler(f.instances, repo, &fakeStreamSelector{chatProv: f.chat, agentProv: f.agent},
		groupResolverResolver(), f.bus, f.queue)
	h.(groupResolutionSetter).SetGroupResolution(f.groups, f.completer)
	repo.arm("cancelled") // recovery closes the record right after the gate read it

	_, err := h.EndSession(convoCtx(), f.parentID)

	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Empty(t, f.completer.all(), "a session recovery closed is never completed by a late end")
	row := f.exec(f.sessionRow)
	assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation), "the empty-output write never reopens it")
	assert.Equal(t, "cancelled", row.LLMConversation[engine.ConversationKeyEndReason])
}
