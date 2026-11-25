package sqlite

import (
	"context"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestStepCommitRepo_NonPortableReceiptNeedsTheNextLocalVersion(t *testing.T) {
	for _, name := range []string{"stale", "gap", "wrong run", "missing"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fx := newStepFixture(t, setupTestDB(t).DB)
			fx.baseline(t)
			require.NoError(t, fx.homes.SetNonPortable(ctx, fx.run, runner.NonPortablePromisorReason))
			base := runner.LocalCheckpointRef{RunID: fx.run, Version: 1, ManifestHash: stepHash('a')}
			require.NoError(t, fx.homes.BootstrapLocalCheckpoint(ctx, fx.run, base.String()))
			j, f := fx.job(t, "node", runner.AffinityWorkspace)
			res := fx.claim(t, j, f)
			ref := base
			switch name {
			case "gap":
				ref.Version = 3
			case "wrong run":
				ref.RunID = uuid.New()
				ref.Version = 2
			}
			receipt := nonPortableReceipt(f, res)
			receipt.Workspace.LocalRef = ref.String()
			if name == "missing" {
				receipt.Workspace.LocalRef = ""
			}
			before := fx.snapshot(t)
			require.ErrorIs(t, fx.repo.CommitStep(ctx, fx.advance("B", receipt)), engine.ErrInvalidStepCompletion)
			assert.Equal(t, before, fx.snapshot(t), "every write rolls back, retaining the live job and claim")
			home, err := fx.homes.GetHome(ctx, fx.run)
			require.NoError(t, err)
			assert.Equal(t, base.String(), home.LocalCheckpoint)
		})
	}
}

func TestStepCommitRepo_NonPortableReceiptAdvancesLocalVersion(t *testing.T) {
	ctx := context.Background()
	fx := newStepFixture(t, setupTestDB(t).DB)
	fx.baseline(t)
	require.NoError(t, fx.homes.SetNonPortable(ctx, fx.run, runner.NonPortablePromisorReason))
	for version := int64(1); version <= 2; version++ {
		if version > 1 {
			inv, err := NewInvocationRepo(fx.db).Begin(ctx, fx.run, "A")
			require.NoError(t, err)
			fx.inv = inv.ID
		}
		j, f := fx.job(t, "node", runner.AffinityWorkspace)
		receipt := nonPortableReceipt(f, fx.claim(t, j, f))
		ref := runner.LocalCheckpointRef{RunID: fx.run, Version: version, ManifestHash: stepHash('a')}.String()
		receipt.Workspace.LocalRef = ref
		c := fx.advance("A", receipt)
		require.NoError(t, fx.repo.CommitStep(ctx, c))
		home, err := fx.homes.GetHome(ctx, fx.run)
		require.NoError(t, err)
		assert.Equal(t, ref, home.LocalCheckpoint)
		assert.False(t, home.Dirty)
		assert.Equal(t, int64(1), home.CheckpointVersion)
		require.NoError(t, fx.repo.CommitStep(ctx, c))
		replay, ok, err := fx.repo.CommittedResult(ctx, fx.inv)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []engine.StepCompletion{receipt}, replay.Completions)
	}
}
