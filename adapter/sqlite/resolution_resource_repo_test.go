package sqlite

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testResolution(attempt int) engine.ResolutionResource {
	id := engine.GroupResolutionIdentity{GroupRunID: uuid.MustParse("11111111-2222-3333-4444-555555555555"), MergeIndex: 1, Attempt: attempt, TaskKey: "b", Open: true}
	rid := engine.ResolutionResourceID(id)
	return engine.ResolutionResource{ID: rid, Identity: id, SourceDir: "/ws/.group-backing/x-gather-1", WorkDir: filepath.Join("/ws/.runner-resources/resolutions", rid.String())}
}

func committedAt(r engine.ResolutionResource, v int) engine.WorkspaceCompletion {
	return engine.WorkspaceCompletion{Ref: r.ID.String() + "@" + strconv.Itoa(v) + ":ab", Resource: engine.ResourceFenceRef{Key: engine.ResolutionResourceKey(r.ID), Owner: "builtin", Generation: 1, Token: uuid.New()}}
}

func TestResolutionResourceRepo_EnsureAndGetRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewResolutionResourceRepo(db.DB)
	r := testResolution(1)
	require.NoError(t, repo.EnsureResolution(context.Background(), r))
	got, err := NewResolutionResourceRepo(db.DB).GetResolution(context.Background(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, r, got, "identity, trusted source dir and canonical workdir persist")
	ref, err := repo.MaterializedRef(context.Background(), r.ID)
	require.NoError(t, err)
	assert.Empty(t, ref, "nothing materialized yet")
}

func TestResolutionResourceRepo_IdempotentEnsureAndMaterialize(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewResolutionResourceRepo(db.DB)
	r := testResolution(1)
	require.NoError(t, repo.EnsureResolution(ctx, r))
	require.NoError(t, repo.EnsureResolution(ctx, r), "a retried prepare is idempotent")
	c := committedAt(r, 2)
	require.NoError(t, repo.MarkMaterialized(ctx, r.ID, c))
	require.NoError(t, repo.MarkMaterialized(ctx, r.ID, c), "a retried materialization is idempotent")
	ref, err := repo.MaterializedRef(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Ref, ref)
}

func TestResolutionResourceRepo_RejectsChangedIdentityOrDirs(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewResolutionResourceRepo(db.DB)
	r := testResolution(1)
	require.NoError(t, repo.EnsureResolution(ctx, r))
	moved := r
	moved.SourceDir = "/ws/parent"
	require.ErrorIs(t, repo.EnsureResolution(ctx, moved), runner.ErrResolutionResourceConflict, "no client may change the source dir")
	forged := r
	forged.ID = uuid.New()
	require.Error(t, repo.EnsureResolution(ctx, forged), "an id not derived from the identity is rejected")
	got, err := repo.GetResolution(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, r, got, "the stored resource is unchanged")
}

func TestResolutionResourceRepo_DatabaseErrorsPropagate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewResolutionResourceRepo(db.DB)
	_, err := repo.GetResolution(context.Background(), uuid.New())
	require.ErrorIs(t, err, instance.ErrNotFound)
	require.NoError(t, db.DB.Close())
	require.Error(t, repo.EnsureResolution(context.Background(), testResolution(1)))
	_, err = repo.GetResolution(context.Background(), testResolution(1).ID)
	require.Error(t, err)
	assert.NotErrorIs(t, err, instance.ErrNotFound, "a closed DB is not reported as a missing row")
}

func TestResolutionResourceRepo_ClosedAttemptRefusesMaterializationAndNewAttemptIsDistinct(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewResolutionResourceRepo(db.DB)
	old, next := testResolution(1), testResolution(2)
	require.NoError(t, repo.EnsureResolution(ctx, old))
	require.NoError(t, repo.CloseResolution(ctx, old.ID))
	require.NoError(t, repo.CloseResolution(ctx, old.ID), "close is idempotent")
	require.ErrorIs(t, repo.MarkMaterialized(ctx, old.ID, committedAt(old, 2)), runner.ErrResolutionClosed)
	ref, err := repo.MaterializedRef(ctx, old.ID)
	require.NoError(t, err)
	assert.Empty(t, ref)
	require.NoError(t, repo.EnsureResolution(ctx, next))
	require.NoError(t, repo.MarkMaterialized(ctx, next.ID, committedAt(next, 2)), "the next attempt is its own open resource")
	require.ErrorIs(t, repo.MarkMaterialized(ctx, uuid.New(), committedAt(next, 2)), instance.ErrNotFound)
}
