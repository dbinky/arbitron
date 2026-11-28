package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeExecutionSession_PatchAtGenerationIsFenced(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	inst, exec := seedSessionExec(t, db, "chat", 1, time.Now().UTC().Truncate(time.Second))
	require.NoError(t, repo.SetSessionState(ctx, exec.ID, instance.SessionState{Generation: 3}))
	ok, err := repo.PatchSessionFields(ctx, exec.ID, map[string]any{"provisionalResume": true})
	require.NoError(t, err)
	require.True(t, ok)
	patch := map[string]any{"provisionalResume": nil, "sessionId": nil, "pendingRebuild": true}

	err = repo.PatchSessionFieldsAtGeneration(ctx, exec.ID, 2, patch)
	require.ErrorIs(t, err, instance.ErrStaleSessionGeneration)
	got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "chat")
	require.NoError(t, err)
	assert.Equal(t, "s1", got.LLMConversation["sessionId"], "a stale generation writes nothing")
	assert.Equal(t, true, got.LLMConversation["provisionalResume"])

	require.NoError(t, repo.PatchSessionFieldsAtGeneration(ctx, exec.ID, 3, patch))
	got, err = repo.GetByInstanceAndNode(ctx, inst.ID, "chat")
	require.NoError(t, err)
	_, hasSID := got.LLMConversation["sessionId"]
	_, hasProv := got.LLMConversation["provisionalResume"]
	assert.False(t, hasSID, "the refused session id is removed")
	assert.False(t, hasProv, "the provisional marker is cleared")
	assert.Equal(t, true, got.LLMConversation["pendingRebuild"])
	assert.Equal(t, "n", got.LLMConversation["nonce"], "every other key is kept")
	assert.EqualValues(t, 3, got.SessionGeneration, "the patch never changes the generation")

	require.ErrorIs(t, repo.PatchSessionFieldsAtGeneration(ctx, uuid.New(), 3, patch), instance.ErrNotFound)
	ok, err = repo.PatchSessionFields(ctx, exec.ID, map[string]any{"endedAt": "2026-11-24T00:00:00Z"})
	require.NoError(t, err)
	require.True(t, ok)
	require.ErrorIs(t, repo.PatchSessionFieldsAtGeneration(ctx, exec.ID, 3, patch), instance.ErrSessionClosed)
}
