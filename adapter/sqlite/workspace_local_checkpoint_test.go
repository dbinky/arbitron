package sqlite

import (
	"context"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

// adapter/sqlite/workspace_home_repo_test.go (package sqlite)
func TestWorkspaceHomeRepo_BootstrapLocalCheckpoint(t *testing.T) {
	ctx := context.Background()
	repo := NewWorkspaceHomeRepo(setupTestDB(t).DB)
	run := uuid.New()
	ref := runner.LocalCheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("a", 64)}.String()

	require.Error(t, repo.BootstrapLocalCheckpoint(ctx, run, ref), "a portable home has no local baseline")
	require.NoError(t, repo.SetNonPortable(ctx, run, runner.NonPortablePromisorReason))
	require.NoError(t, repo.MarkDirty(ctx, run, true))
	require.NoError(t, repo.BootstrapLocalCheckpoint(ctx, run, ref))

	h, err := repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, ref, h.LocalCheckpoint)
	assert.False(t, h.Dirty, "the copy matches the working directory")
	assert.Equal(t, runner.ServerHome, h.RunnerID)
	assert.Zero(t, h.Generation, "no ownership change")
	require.NoError(t, repo.BootstrapLocalCheckpoint(ctx, run, ref), "idempotent")

	other := runner.LocalCheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("b", 64)}.String()
	require.ErrorIs(t, repo.BootstrapLocalCheckpoint(ctx, run, other), runner.ErrCheckpointConflict, "never overwrites a committed base")
	v2 := runner.LocalCheckpointRef{RunID: run, Version: 2, ManifestHash: strings.Repeat("a", 64)}.String()
	require.ErrorIs(t, repo.BootstrapLocalCheckpoint(ctx, uuid.New(), v2), runner.ErrCheckpointConflict, "wrong run / not version 1")
	hist, err := repo.History(ctx, run)
	require.NoError(t, err)
	assert.Empty(t, hist)
}

func TestWorkspaceHomeRepo_BootstrapLocalCheckpointRefusesAnActiveWriter(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewWorkspaceHomeRepo(db.DB)
	run := uuid.New()
	require.NoError(t, repo.SetNonPortable(ctx, run, runner.NonPortablePromisorReason))
	_, err := NewResourceLeaseRepo(db.DB, "boot-local-checkpoint").Claim(ctx, "run:"+run.String(), "holder-1", runner.BuiltInRunnerID)
	require.NoError(t, err)
	ref := runner.LocalCheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("a", 64)}.String()
	require.ErrorIs(t, repo.BootstrapLocalCheckpoint(ctx, run, ref), runner.ErrCheckpointConflict)
	h, err := repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Empty(t, h.LocalCheckpoint)
}
