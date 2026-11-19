package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cascadeTree creates a root and one fire-and-forget child in state st.
func cascadeTree(t *testing.T, repo *InstanceRepo, st instance.State) (root, child uuid.UUID) {
	t.Helper()
	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(context.Background(), p))
	c := childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, st,
		uuid.MustParse(spawnTestOwner), instance.VisibilityPrivate, "")
	return p.ID, c
}

func TestMarkCascadePaused_FromRunningWaitingQueued(t *testing.T) {
	for _, st := range []instance.State{instance.StateRunning, instance.StateWaiting, instance.StateQueued} {
		t.Run(string(st), func(t *testing.T) {
			repo := NewInstanceRepo(setupTestDB(t).DB)
			ctx := context.Background()
			root, child := cascadeTree(t, repo, st)

			ok, err := repo.MarkCascadePaused(ctx, child, root)
			require.NoError(t, err)
			assert.True(t, ok)

			got, err := repo.Get(ctx, child)
			require.NoError(t, err)
			assert.Equal(t, instance.StatePaused, got.State)

			refs, err := repo.ListChildren(ctx, root)
			require.NoError(t, err)
			require.Len(t, refs, 1)
			require.NotNil(t, refs[0].PausedBy)
			assert.Equal(t, root, *refs[0].PausedBy)

			from, claimed, err := repo.ClaimCascadePause(ctx, child, root)
			require.NoError(t, err)
			assert.True(t, claimed)
			assert.Equal(t, st, from)

			by, err := repo.CascadePausedBy(ctx, child)
			require.NoError(t, err)
			assert.Nil(t, by, "claim must clear the marker")
		})
	}
}

func TestMarkCascadePaused_TerminalOrPaused_NoChange(t *testing.T) {
	for _, st := range []instance.State{instance.StateCompleted, instance.StateFailed, instance.StateCancelled, instance.StatePaused} {
		t.Run(string(st), func(t *testing.T) {
			repo := NewInstanceRepo(setupTestDB(t).DB)
			ctx := context.Background()
			root, child := cascadeTree(t, repo, st)

			ok, err := repo.MarkCascadePaused(ctx, child, root)
			require.NoError(t, err)
			assert.False(t, ok)

			got, err := repo.Get(ctx, child)
			require.NoError(t, err)
			assert.Equal(t, st, got.State)

			by, err := repo.CascadePausedBy(ctx, child)
			require.NoError(t, err)
			assert.Nil(t, by)
		})
	}
}

func TestClaimCascadePause_WrongAncestor_NotClaimed(t *testing.T) {
	repo := NewInstanceRepo(setupTestDB(t).DB)
	ctx := context.Background()
	root, child := cascadeTree(t, repo, instance.StateRunning)
	_, err := repo.MarkCascadePaused(ctx, child, root)
	require.NoError(t, err)

	_, claimed, err := repo.ClaimCascadePause(ctx, child, uuid.New())
	require.NoError(t, err)
	assert.False(t, claimed)

	by, err := repo.CascadePausedBy(ctx, child)
	require.NoError(t, err)
	require.NotNil(t, by)
	assert.Equal(t, root, *by)
}

func TestClearCascadePause_ClearsMarker(t *testing.T) {
	repo := NewInstanceRepo(setupTestDB(t).DB)
	ctx := context.Background()
	root, child := cascadeTree(t, repo, instance.StateRunning)
	_, err := repo.MarkCascadePaused(ctx, child, root)
	require.NoError(t, err)

	require.NoError(t, repo.ClearCascadePause(ctx, child))

	by, err := repo.CascadePausedBy(ctx, child)
	require.NoError(t, err)
	assert.Nil(t, by)
	_, claimed, err := repo.ClaimCascadePause(ctx, child, root)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestCascadePause_SurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arb.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	repo := NewInstanceRepo(db)
	ctx := context.Background()
	root, child := cascadeTree(t, repo, instance.StateRunning)
	_, err = repo.MarkCascadePaused(ctx, child, root)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db2, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db2.Close() })
	require.NoError(t, RunMigrations(db2))
	by, err := NewInstanceRepo(db2).CascadePausedBy(ctx, child)
	require.NoError(t, err)
	require.NotNil(t, by)
	assert.Equal(t, root, *by)
}
