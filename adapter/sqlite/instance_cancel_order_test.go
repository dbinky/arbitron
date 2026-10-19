package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing the durable creation/intent boundary must include the post-request
// child here: all timestamps are deliberately identical, without clock sleeps.
func TestListOrphanedActiveChildren_SameSecondIntentBoundary(t *testing.T) {
	for _, mode := range []instance.SpawnMode{instance.SpawnModeFireAndForget, instance.SpawnModeWaitForChild, instance.SpawnModeGroupTask} {
		for _, state := range []instance.State{instance.StateCompleted, instance.StateFailed} {
			t.Run(string(mode)+"/"+string(state), func(t *testing.T) {
				db := setupTestDB(t)
				repo := NewInstanceRepo(db.DB)
				ctx := context.Background()
				root := newLinkedInstance(instance.SpawnLink{})
				root.State = state
				require.NoError(t, repo.Create(ctx, root))
				mid := childOf(t, repo, root.ID, "mid", instance.SpawnModeFireAndForget, instance.StateCompleted,
					uuid.MustParse(spawnTestOwner), instance.VisibilityPrivate, "")
				create := func(key string) uuid.UUID {
					if mode == instance.SpawnModeGroupTask {
						return createGroupTask(t, repo, mid, "group-"+key, uuid.New(), key, instance.StateWaiting)
					}
					return childOf(t, repo, mid, key, mode, instance.StateWaiting,
						uuid.MustParse(spawnTestOwner), instance.VisibilityPrivate, "")
				}
				before := create("before")
				require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, root.ID))
				after := create("after")
				stamp := subtreeStamp(t, db, root.ID)
				_, err := db.DB.Exec(`UPDATE instances SET created_at = ? WHERE id IN (?, ?)`, stamp, before.String(), after.String())
				require.NoError(t, err)

				got, err := repo.ListOrphanedActiveChildren(ctx)
				require.NoError(t, err)
				assert.Equal(t, []uuid.UUID{before}, got, "only the pre-request descendant is covered")

				// A second request must advance the boundary even in the same second.
				require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, root.ID))
				got, err = repo.ListOrphanedActiveChildren(ctx)
				require.NoError(t, err)
				assert.Equal(t, []uuid.UUID{before, after}, got)
			})
		}
	}
}

func TestListOrphanedActiveChildren_DeletedNewestChildCannotRewindIntentBoundary(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, root))
	owner := uuid.MustParse(spawnTestOwner)
	before := childOf(t, repo, root.ID, "before", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")
	require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, root.ID))
	_, err := db.DB.Exec(`DELETE FROM instances WHERE id = ?`, before.String())
	require.NoError(t, err)
	after := childOf(t, repo, root.ID, "after", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")
	_, err = db.DB.Exec(`UPDATE instances SET created_at = ? WHERE id = ?`, subtreeStamp(t, db, root.ID), after.String())
	require.NoError(t, err)
	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "purging history must not cover a later child with an earlier intent")
}

func TestMarkSubtreeCancelRequested_BoundaryWriteFailureRollsBackStamp(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, root))
	_, err := db.DB.Exec(`CREATE TRIGGER reject_cancel_boundary
BEFORE UPDATE OF cancel_through_sequence ON instance_creation_order
BEGIN SELECT RAISE(ABORT, 'injected boundary failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.MarkSubtreeCancelRequested(ctx, root.ID), "injected boundary failure")
	assert.Nil(t, subtreeStamp(t, db, root.ID), "the audit stamp cannot commit without its recovery boundary")
}

func TestInstanceCreationOrder_WriteFailureRollsBackChild(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	_, err := db.DB.Exec(`CREATE TRIGGER reject_creation_order
BEFORE INSERT ON instance_creation_order
BEGIN SELECT RAISE(ABORT, 'injected creation-order failure'); END`)
	require.NoError(t, err)
	child := newLinkedInstance(instance.SpawnLink{})
	require.ErrorContains(t, repo.Create(context.Background(), child), "injected creation-order failure")
	_, err = repo.Get(context.Background(), child.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound, "no child may commit without its durable creation order")
}

func TestMigration021_PreservesLegacyIntentAndBackfillsCreationOrder(t *testing.T) {
	db := setupTestDB(t)
	// Restore the previous schema, then populate it through the existing producer.
	down, err := migrationsFS.ReadFile("migrations/021_instance_cancel_order.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	root := newLinkedInstance(instance.SpawnLink{})
	root.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, root))
	owner := uuid.MustParse(spawnTestOwner)
	before := childOf(t, repo, root.ID, "before", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")
	after := childOf(t, repo, root.ID, "after", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")
	_, err = db.DB.Exec(`UPDATE instances SET created_at = '1999-01-01T00:00:00Z' WHERE id = ?`, before.String())
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE instances SET created_at = '2001-01-01T00:00:00Z' WHERE id = ?`, after.String())
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE instances SET subtree_cancel_requested_at = '2000-01-01T00:00:00Z' WHERE id = ?`, root.ID.String())
	require.NoError(t, err)
	up, err := migrationsFS.ReadFile("migrations/021_instance_cancel_order.up.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(up))
	require.NoError(t, err)
	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{before}, got, "the upgrade preserves legacy intent coverage")

	later := childOf(t, repo, root.ID, "later", instance.SpawnModeFireAndForget, instance.StateWaiting, owner, instance.VisibilityPrivate, "")
	_, err = db.DB.Exec(`UPDATE instances SET created_at = '2000-01-01T00:00:00Z' WHERE id = ?`, later.String())
	require.NoError(t, err)
	got, err = repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{before}, got, "a post-upgrade child cannot inherit an old intent even with an older clock")
}
