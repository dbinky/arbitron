package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- ListChildren ----------

// EVERY child, terminal ones included, with its current state, in creation
// order. Terminal children are the whole point: a completed branch or a
// completed fire-and-forget child may still own active descendants the cancel
// cascade must reach.
func TestListChildren_ReturnsEveryChildWithStateInCreationOrder(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	states := []instance.State{
		instance.StateCompleted, instance.StateRunning, instance.StateFailed,
		instance.StateCancelled, instance.StateQueued, instance.StateWaiting, instance.StatePaused,
	}
	want := make([]instance.ChildRef, 0, len(states))
	for i, st := range states {
		mode := instance.SpawnModeFireAndForget
		if i%2 == 1 {
			mode = instance.SpawnModeWaitForChild
		}
		id := childOf(t, repo, p.ID, fmt.Sprintf("n%d", i), mode, st, owner, instance.VisibilityPrivate, "")
		want = append(want, instance.ChildRef{ID: id, State: st})
	}

	got, err := repo.ListChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, want, got,
		"every child must be returned — terminal ones too — in creation order with its state")
}

func TestListChildren_NoChildrenReturnsEmptySlice(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.ListChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.NotNil(t, got, "must return an empty slice, not nil")
	assert.Empty(t, got)
}

// ONE level, and only this parent's children: recursion belongs to the domain
// cascade, which carries the visited set.
func TestListChildren_IsOneLevelAndScopedToTheParent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	other := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, other))

	child := childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, other.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListChildren(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, instance.ChildRef{ID: child, State: instance.StateCompleted}, got[0])
}

func TestListChildren_StorageFailureSurfacesAsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, db.DB.Close())

	_, err := repo.ListChildren(ctx, p.ID)
	require.Error(t, err, "a storage failure must never look like \"no children\"")
}

// ---------- ListOrphanedActiveChildren (recursive) ----------

// cancelled root -> completed child -> failed grandchild -> RUNNING
// great-grandchild. The old one-level join could never see this run: its own
// parent is failed, not cancelled. The climb through terminal intermediates is
// what finds it.
func TestListOrphanedActiveChildren_FindsGreatGrandchildUnderCancelledRootThroughTerminalIntermediates(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCancelled
	require.NoError(t, repo.Create(ctx, root))
	child := childOf(t, repo, root.ID, "n1", instance.SpawnModeWaitForChild, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	grand := childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateFailed, owner, instance.VisibilityPrivate, "")
	great := childOf(t, repo, grand, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{great}, got)
}

// An ACTIVE intermediate stops the climb: the intermediate is itself the
// orphan, and cancelling it cascades to its own active children. Returning the
// grandchild as well would only produce a benign ErrCannotCancel later, but the
// query keeps the old "topmost stranded run" shape.
func TestListOrphanedActiveChildren_ActiveIntermediateIsTheOrphanNotItsChildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCancelled
	require.NoError(t, repo.Create(ctx, root))
	child := childOf(t, repo, root.ID, "n1", instance.SpawnModeWaitForChild, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{child}, got)
}

// A COMPLETED root's still-running descendants are legitimate independent work
// (a finish_independently loser, a fire-and-forget child). No cancelled
// ancestor means no cancellation intent: the sweep must never become a
// run-killer.
func TestListOrphanedActiveChildren_IgnoresIndependentWorkUnderCompletedAncestors(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, root))
	child := childOf(t, repo, root.ID, "n1", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, root.ID, "n2", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Several stranded runs under one cancelled root are all returned, oldest first.
func TestListOrphanedActiveChildren_ReturnsAllStrandedRunsOldestFirst(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCancelled
	require.NoError(t, repo.Create(ctx, root))
	mid := childOf(t, repo, root.ID, "n1", instance.SpawnModeWaitForChild, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	first := childOf(t, repo, mid, "n1", instance.SpawnModeFireAndForget, instance.StateQueued, owner, instance.VisibilityPrivate, "")
	second := childOf(t, repo, mid, "n2", instance.SpawnModeFireAndForget, instance.StatePaused, owner, instance.VisibilityPrivate, "")
	direct := childOf(t, repo, root.ID, "n2", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{first, second, direct}, got)
}

// Corrupt link data (an ancestor cycle through terminal rows) must terminate
// the recursive CTE instead of looping; parent_instance_id deliberately has no
// foreign key, so such rows are storable.
func TestListOrphanedActiveChildren_CorruptAncestorCycleTerminates(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	aID, bID := uuid.New(), uuid.New()
	a := newLinkedInstance(instance.SpawnLink{ParentInstanceID: bID, ParentNodeID: "n", Mode: instance.SpawnModeFireAndForget})
	a.ID = aID
	a.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, a))
	b := newLinkedInstance(instance.SpawnLink{ParentInstanceID: aID, ParentNodeID: "n", Mode: instance.SpawnModeFireAndForget})
	b.ID = bID
	b.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, b))
	childOf(t, repo, aID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err, "the bounded climb must terminate on a cycle")
	assert.Empty(t, got)
}

// ---------- durable subtree-cancellation intent ----------

func subtreeStamp(t *testing.T, db testDB, id uuid.UUID) any {
	t.Helper()
	var v any
	require.NoError(t, db.DB.QueryRow(`SELECT subtree_cancel_requested_at FROM instances WHERE id = ?`,
		id.String()).Scan(&v))
	return v
}

// The stamp lands on terminal rows only, in RFC3339 layout; an active row
// (whose own cancellation IS the intent) and an unknown id are left alone.
func TestMarkSubtreeCancelRequested_StampsTerminalRowsOnly(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	done := newLinkedInstance(instance.SpawnLink{})
	done.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, done))
	active := newLinkedInstance(instance.SpawnLink{})
	active.State = instance.StateRunning
	require.NoError(t, repo.Create(ctx, active))

	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, done.ID))
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, active.ID))
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, uuid.New()), "an unknown id is not an error")

	stamp, ok := subtreeStamp(t, db, done.ID).(string)
	require.True(t, ok, "a terminal row must carry the stamp")
	_, err := parseTime(stamp)
	assert.NoError(t, err, "the audit timestamp remains RFC3339; recovery orders by sequence")
	assert.Nil(t, subtreeStamp(t, db, active.ID), "an active row is never stamped")
}

func TestMarkSubtreeCancelRequested_StorageFailureSurfacesAsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	require.NoError(t, db.DB.Close())
	require.Error(t, repo.MarkSubtreeCancelRequested(context.Background(), uuid.New()))
}

// THE DURABILITY ASSERTION: a COMPLETED root whose subtree cancellation was
// requested (stamped) is cancellation intent for the run the interrupted walk
// left behind, through a completed intermediate.
func TestListOrphanedActiveChildren_StampedTerminalAncestorIsCancellationIntent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, root))
	mid := childOf(t, repo, root.ID, "n1", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	leaf := childOf(t, repo, mid, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, root.ID))

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{leaf}, got)
}

// A run created AFTER the stamp (e.g. by a parent reopened through Retry) was
// never covered by the request and is not swept.
func TestListOrphanedActiveChildren_RunCreatedAfterTheStampIsNotSwept(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateFailed
	require.NoError(t, repo.Create(ctx, root))
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, root.ID))
	childOf(t, repo, root.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "the child was created after the request and carries no cancellation intent")
}
