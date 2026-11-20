package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ instance.SpawnLinkRepository = (*InstanceRepo)(nil)

// runningParent creates a parent in the running state at nodeID: the shape a
// parent is left in when it is stranded at an awaited spawn node.
func runningParent(t *testing.T, repo *InstanceRepo, nodeID string) uuid.UUID {
	t.Helper()
	id := waitingParent(t, repo, nodeID)
	require.NoError(t, repo.UpdateState(context.Background(), id, instance.StateRunning))
	return id
}

func stateOfRow(t *testing.T, repo *InstanceRepo, id uuid.UUID) instance.State {
	t.Helper()
	got, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	return got.State
}

// ---------- ListRunningOnTerminalChild ----------

func TestListRunningOnTerminalChild_FindsRunningParentAtNodeWithFailedChild(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := runningParent(t, repo, "spawn-1")
	child := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateFailed, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListRunningOnTerminalChild(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, parent, got[0].ParentID)
	assert.Equal(t, "spawn-1", got[0].ParentNodeID)
	assert.Equal(t, child, got[0].ChildID)
	assert.Equal(t, instance.StateFailed, got[0].ChildState)
}

func TestListRunningOnTerminalChild_IgnoresActiveChild(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := runningParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListRunningOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListRunningOnTerminalChild_IgnoresWaitingParent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateFailed, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListRunningOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "a waiting parent belongs to ListWaitingOnTerminalChild")
}

func TestListRunningOnTerminalChild_IgnoresOtherNodeAndOtherMode(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	atB := runningParent(t, repo, "spawn-B")
	childOf(t, repo, atB, "spawn-A", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	fnf := runningParent(t, repo, "spawn-1")
	childOf(t, repo, fnf, "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateFailed, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListRunningOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ---------- SuspendStrandedAt ----------

func TestSuspendStrandedAt_MovesRunningAtNodeToWaiting(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	parent := runningParent(t, repo, "spawn-1")
	ok, err := repo.SuspendStrandedAt(context.Background(), parent, "spawn-1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, instance.StateWaiting, stateOfRow(t, repo, parent))
}

func TestSuspendStrandedAt_WrongNode_NoChange(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	parent := runningParent(t, repo, "spawn-2")
	ok, err := repo.SuspendStrandedAt(context.Background(), parent, "spawn-1")
	require.NoError(t, err)
	assert.False(t, ok, "a parent that moved on to a later node is not the stranded one")
	assert.Equal(t, instance.StateRunning, stateOfRow(t, repo, parent))
}

func TestSuspendStrandedAt_NotRunning_NoChange(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	parent := runningParent(t, repo, "spawn-1")
	require.NoError(t, repo.UpdateState(context.Background(), parent, instance.StatePaused))
	ok, err := repo.SuspendStrandedAt(context.Background(), parent, "spawn-1")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, instance.StatePaused, stateOfRow(t, repo, parent))
}
