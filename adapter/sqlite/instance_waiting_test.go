package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The drain inventory needs both the current node and its workflow snapshot.
func TestInstanceRepo_ListWaitingAtNode(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewInstanceRepo(db.DB)
	owner, err := NewUserRepo(db.DB).GetDefaultUser(ctx)
	require.NoError(t, err)

	waiting := newTestInstance(t, owner)
	waiting.WorkflowSnapshot["nodes"] = []any{map[string]any{
		"id": "chat", "type": "interactive_agent", "config": map[string]any{},
	}}
	require.NoError(t, repo.Create(ctx, waiting))
	require.NoError(t, repo.UpdateCurrentNode(ctx, waiting.ID, "chat"))
	require.NoError(t, repo.UpdateState(ctx, waiting.ID, instance.StateWaiting))
	running := newTestInstance(t, owner)
	require.NoError(t, repo.Create(ctx, running))
	require.NoError(t, repo.UpdateCurrentNode(ctx, running.ID, "chat"))

	got, err := repo.ListWaitingAtNode(ctx)
	require.NoError(t, err)
	ids := map[uuid.UUID]instance.Instance{}
	for _, i := range got {
		ids[i.ID] = i
	}
	require.Contains(t, ids, waiting.ID)
	assert.NotContains(t, ids, running.ID)
	w := ids[waiting.ID]
	require.NotNil(t, w.CurrentNodeID)
	assert.Equal(t, "chat", *w.CurrentNodeID)
	assert.Equal(t, "test-workflow", w.WorkflowSnapshot["name"], "the snapshot is read for node-type resolution")

	// Drive the actual database producer into the inventory's count sink.
	inventory, err := runner.NewInteractiveSessionInventory(repo, NewGroupRunRepo(db.DB))
	require.NoError(t, err)
	n, err := inventory.CountPinned(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the stored interactive wait blocks built-in drain")
	require.NoError(t, repo.UpdateState(ctx, waiting.ID, instance.StateCompleted))
	n, err = inventory.CountPinned(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Zero(t, n, "a closed session no longer blocks drain")
}
