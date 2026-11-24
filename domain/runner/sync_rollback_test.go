package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceSync_StaleFailureCannotRestoreNewWritersFiles(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "committed"})
	old := f.reserve(t, builtin)
	att := f.begin(t, old, stay(builtin))
	ctx := context.Background()
	job, err := f.pool.Ledger.Requeue(ctx, old.Fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	require.NoError(t, f.leases.Release(ctx, att.Resource))
	current := f.reserveJob(t, job.ID, builtin)
	currentAtt := f.begin(t, current, stay(builtin))
	f.write(t, "a.txt", "current writer's changes")

	require.Error(t, f.sync.FinishFailure(ctx, old, att))
	assert.Equal(t, "current writer's changes", f.read(t, "a.txt"))
	assert.True(t, f.home(t).Dirty)
	rec, ok := f.active(t)
	require.True(t, ok)
	assert.Equal(t, currentAtt.Resource, rec.Fence)
	assert.Equal(t, base, f.latest(t))
}
