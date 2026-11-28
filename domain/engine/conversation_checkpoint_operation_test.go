package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationHandler_CheckpointEndExistingOutputCannotBypassCommit(t *testing.T) {
	instRepo, execs, llm, bus, queue, id := conversationTestSetup(t)
	before, err := execs.GetByInstanceAndNode(context.Background(), id, "interact")
	require.NoError(t, err)
	before.Output = map[string]any{"answer": "already set"}
	prior, pin := "session:prior-native-bundle", "mac-a"
	before.SessionCheckpointRef, before.SessionPinRunner = &prior, &pin
	h := buildConversationHandler(instRepo, execs, llm, bus, queue)
	f := newCkFixture()
	intents := wiredHandler(t, h, f)
	f.committer.err = engine.ErrTurnFenced
	_, err = h.EndSession(convoCtx(), id)
	require.ErrorIs(t, err, engine.ErrTurnFenced)
	assert.Empty(t, intents.completed)
	assert.Empty(t, bus.eventsOfType(id, engine.EventTypeInteractiveEnded))
	inst, err := instRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, inst.State)
	f.committer.err = nil
	_, err = h.EndSession(convoCtx(), id)
	require.NoError(t, err)
	require.Len(t, f.committer.commits, 1)
	c := f.committer.commits[0]
	assert.Empty(t, c.Completions)
	assert.Equal(t, before.Output, c.Execution.Output)
	assert.Equal(t, prior, c.CheckpointRef)
	assert.Equal(t, pin, c.PinRunner)
	assert.Empty(t, f.exporter.reqs, "End reuses the committed pair without exporting")
	assert.Equal(t, intents.persisted[1].InvocationID, c.InvocationID)
	assert.NotEmpty(t, c.InvocationID)
	assert.Empty(t, intents.completed)
}

func TestConversationHandler_CheckpointAbandonCommitsBeforeClosure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "commit failure"}[failed], func(t *testing.T) {
			g := newGroupConvoFixture(t, true, true)
			f := newCkFixture()
			intents := wiredHandler(t, g.handler, f)
			f.committer.apply = func(ex *instance.NodeExecution) error { return g.nodeExecs.Update(context.Background(), ex) }
			if failed {
				f.committer.err = errors.New("commit unavailable")
			}
			err := g.handler.AbandonSession(convoCtx(), g.parentID)
			if failed {
				require.ErrorContains(t, err, "commit unavailable")
				assert.Empty(t, g.completer.all())
				assert.True(t, engine.IsOpenGroupResolutionSession(g.exec(g.sessionRow).LLMConversation))
				assert.Empty(t, g.bus.eventsOfType(g.parentID, engine.EventTypeInteractiveEnded))
			} else {
				require.NoError(t, err)
				require.Len(t, f.committer.commits, 1)
				c := f.committer.commits[0]
				assert.Empty(t, c.Completions)
				assert.True(t, c.RequireOpenSession)
				assert.Equal(t, intents.persisted[0].ID, c.IntentID)
				assert.NotEmpty(t, c.InvocationID)
				assert.False(t, engine.IsOpenGroupResolutionSession(g.exec(g.sessionRow).LLMConversation))
				assert.Len(t, g.completer.all(), 1)
			}
			assert.Empty(t, intents.completed)
			assert.Empty(t, f.exporter.reqs)
		})
	}
}
