package sqlite

import (
	"context"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNodeExecutionRepo_PlacementSessionCAS(t *testing.T) {
	db := setupTestDB(t)
	owner := seedUser(t, db, "owner-"+uuid.NewString()[:8])
	id := seedInstance(t, db, owner, "running")
	repo := NewNodeExecutionRepo(db.DB)
	ctx := context.Background()
	e := placedExec(id, "B", 1)
	require.NoError(t, repo.Create(ctx, e))
	e.SourceID = nil
	e.PlacementHistory = append(e.PlacementHistory, instance.PlacementRecord{JobID: "followup", RunnerID: "builtin", Outcome: "completed"})
	e.LLMConversation = map[string]any{"messages": []any{"new turn"}}
	written, err := repo.UpdateIfSessionOpen(ctx, e)
	require.NoError(t, err)
	require.True(t, written)
	got, err := repo.GetByInstanceAndNode(ctx, id, "B")
	require.NoError(t, err)
	require.Nil(t, got.SourceID)
	require.Len(t, got.PlacementHistory, 3)
	_, err = repo.PatchSessionFields(ctx, e.ID, map[string]any{"endedAt": "closed"})
	require.NoError(t, err)
	e.PlacementHistory = append(e.PlacementHistory, instance.PlacementRecord{JobID: "stale"})
	e.LLMConversation = map[string]any{"messages": []any{"stale turn"}}
	written, err = repo.UpdateIfSessionOpen(ctx, e)
	require.NoError(t, err)
	require.False(t, written)
	got, err = repo.GetByInstanceAndNode(ctx, id, "B")
	require.NoError(t, err)
	require.Len(t, got.PlacementHistory, 3)
	require.Equal(t, "closed", got.LLMConversation["endedAt"])
	require.Equal(t, []any{"new turn"}, got.LLMConversation["messages"])
}
