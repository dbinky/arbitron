package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ instance.SpawnWaitRepository = (*InstanceRepo)(nil)

// waitingParent creates a parent in the waiting state whose current node is the
// given node id.
func waitingParent(t *testing.T, repo *InstanceRepo, nodeID string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	p := newLinkedInstance(instance.SpawnLink{})
	p.State = instance.StateWaiting
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, repo.UpdateCurrentNode(ctx, p.ID, nodeID))
	require.NoError(t, repo.UpdateState(ctx, p.ID, instance.StateWaiting))
	return p.ID
}

// ---------- FindChildBySpawnNode ----------

func TestFindChildBySpawnNode_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	child := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.FindChildBySpawnNode(ctx, parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, child, got.ChildID)
	assert.Equal(t, instance.StateRunning, got.ChildState)
	assert.False(t, got.ChildState.IsTerminal())
}

func TestFindChildBySpawnNode_NoChildReturnsNilNilNotError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parent := waitingParent(t, repo, "spawn-1")

	got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.NoError(t, err, "\"no child yet\" is the FIRST-EXECUTION signal, not an error")
	assert.Nil(t, got)
}

// The node id is part of the key: a child from a DIFFERENT spawn node of the
// same parent must not be matched.
func TestFindChildBySpawnNode_DifferentNodeDoesNotMatch(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-B")
	childOf(t, repo, parent, "spawn-A", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-B", instance.SpawnModeWaitForChild)
	require.NoError(t, err)
	assert.Nil(t, got, "node A's child must not answer for node B")
}

// The mode is part of the key: a fire-and-forget child of the SAME node must
// not be matched by an awaited lookup.
func TestFindChildBySpawnNode_DifferentModeDoesNotMatch(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindChildBySpawnNode_TerminalChildIsReturnedWithItsState(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	for _, st := range []instance.State{
		instance.StateCompleted, instance.StateFailed, instance.StateCancelled,
	} {
		parent := waitingParent(t, repo, "spawn-1")
		childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild, st, owner, instance.VisibilityPrivate, "")

		got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, st, got.ChildState)
		assert.True(t, got.ChildState.IsTerminal())
	}
}

// A node-level retry after a partially failed suspension can leave more than
// one child row for the same (parent, node, mode) key. created_at has only
// second resolution (strftime %S, migration 001), so two children created
// within the same test — as a real retry typically is — land on an identical
// timestamp; rowid DESC is the ONLY thing that breaks the tie. Without this
// test, dropping that tie-break (or reversing it) would silently make the
// executor's resumption short-circuit key on the WRONG child and no test
// would fail.
func TestFindChildBySpawnNode_MultipleChildrenReturnsTheNewest(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	stale := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateFailed, owner, instance.VisibilityPrivate, "")
	newest := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, newest, got.ChildID, "the retry's newest child must answer, not the stale one")
	assert.NotEqual(t, stale, got.ChildID)
	assert.Equal(t, instance.StateRunning, got.ChildState)
}

func TestFindChildBySpawnNode_StorageFailureErrors(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parent := waitingParent(t, repo, "spawn-1")
	require.NoError(t, db.DB.Close())

	_, err := repo.FindChildBySpawnNode(context.Background(), parent, "spawn-1", instance.SpawnModeWaitForChild)
	require.Error(t, err, "a storage failure must not be indistinguishable from \"no child yet\", "+
		"which would cause a SECOND child to be spawned")
}

// ---------- ListWaitingOnTerminalChild ----------

func TestListWaitingOnTerminalChild_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	child := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateFailed, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, parent, got[0].ParentID)
	assert.Equal(t, "spawn-1", got[0].ParentNodeID)
	assert.Equal(t, child, got[0].ChildID)
	assert.Equal(t, instance.StateFailed, got[0].ChildState)
}

func TestListWaitingOnTerminalChild_ActiveChildIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "a parent whose child is still running is correctly waiting")
}

// The three-part match: a parent waiting at node B is NOT a candidate because
// node A's child finished.
func TestListWaitingOnTerminalChild_NodeMismatchIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-B")
	childOf(t, repo, parent, "spawn-A", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListWaitingOnTerminalChild_FireAndForgetIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "a fire-and-forget child must never wake its parent")
}

func TestListWaitingOnTerminalChild_NonWaitingParentIsNotACandidate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	require.NoError(t, repo.UpdateState(ctx, parent, instance.StateCancelled))
	childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "a cancelled parent must never be resurrected by its dying child")
}

// A node-level retry after a partially failed suspension can leave more than
// one terminal child row for the same (parent, node, mode) key — the same
// situation FindChildBySpawnNode's tie-break exists for. The JOIN here has no
// analogous tie-break: it is not resolving "which child answers", only
// "is there a terminal child at all", so one row per matching child is
// correct, not a bug. This pins that shape down so a future change collapsing
// it to one-row-per-parent (or, worse, silently dropping the newest) is
// caught, and documents that the duplicate is harmless downstream (see
// TestSpawnWaitReconciler_DuplicateCandidatesForSameParentAreHandledOnce).
func TestListWaitingOnTerminalChild_RetryLeavesOneRowPerTerminalChild(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse(spawnTestOwner)

	parent := waitingParent(t, repo, "spawn-1")
	stale := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateFailed, owner, instance.VisibilityPrivate, "")
	newest := childOf(t, repo, parent, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateCompleted, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2, "both terminal children of the retried node are candidates")

	childIDs := []uuid.UUID{got[0].ChildID, got[1].ChildID}
	assert.ElementsMatch(t, []uuid.UUID{stale, newest}, childIDs)
	for _, c := range got {
		assert.Equal(t, parent, c.ParentID)
		assert.Equal(t, "spawn-1", c.ParentNodeID)
	}
}

func TestListWaitingOnTerminalChild_EmptyDatabaseReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	got, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestListWaitingOnTerminalChild_StorageFailureErrors(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	require.NoError(t, db.DB.Close())

	_, err := repo.ListWaitingOnTerminalChild(context.Background())
	require.Error(t, err)
}
