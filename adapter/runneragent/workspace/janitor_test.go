package workspace_test

import (
	"context"
	"os"
	"testing"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncerJanitor_NeverDeletesANewerCopy(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	job, err := f.srv.Plan(ctx, f.workDir, 2)
	require.NoError(t, err)
	local, err := f.sync.Pull(ctx, job)
	require.NoError(t, err)
	held, err := f.sync.HeldCopies(ctx)
	require.NoError(t, err)
	assert.Equal(t, []runneragent.HeldCopy{{RunID: f.runID.String(), HomeGeneration: 2}}, held)
	deleted, err := f.sync.DeleteCopy(ctx, f.runID.String(), 1)
	require.NoError(t, err)
	assert.False(t, deleted, "a delete for generation 1 cannot remove the generation-2 copy")
	_, err = os.Stat(local)
	require.NoError(t, err)
	deleted, err = f.sync.DeleteCopy(ctx, f.runID.String(), 2)
	require.NoError(t, err)
	assert.True(t, deleted)
	_, err = os.Stat(local)
	assert.True(t, os.IsNotExist(err), "the local directory is gone")
	held, err = f.sync.HeldCopies(ctx)
	require.NoError(t, err)
	assert.Empty(t, held, "no longer reported in hello")
	deleted, err = f.sync.DeleteCopy(ctx, f.runID.String(), 9)
	require.NoError(t, err)
	assert.False(t, deleted, "deleting an absent copy is a no-op")
}
