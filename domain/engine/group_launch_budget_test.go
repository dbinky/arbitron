package engine_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture's parent area holds README.md (11 bytes) and src/main.go (13
// bytes); a two-task launch makes three copies of it (snapshot + two forks).
const glParentAreaBytes = 24

func TestGroupLauncher_ByteBudgetRefusesBeforeAnythingIsCopied(t *testing.T) {
	f := newLaunchFixture(t)
	f.launcher.SetLaunchByteBudget(3*glParentAreaBytes-1, filesystem.GroupAreaMeter{})

	run, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.Error(t, err)
	assert.Nil(t, run)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, engine.ErrGroupLaunchBudget)
	assert.Empty(t, f.groupWS.snapshotHandles(), "the budget is checked before the snapshot")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_ByteBudgetAdmitsALaunchWithinIt(t *testing.T) {
	f := newLaunchFixture(t)
	f.launcher.SetLaunchByteBudget(3*glParentAreaBytes, filesystem.GroupAreaMeter{})

	run, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	require.NotNil(t, run)
}

func TestGroupLauncher_SetLaunchByteBudgetRequiresAPositiveBudgetAndAMeter(t *testing.T) {
	f := newLaunchFixture(t)
	assert.Panics(t, func() { f.launcher.SetLaunchByteBudget(0, filesystem.GroupAreaMeter{}) })
	assert.Panics(t, func() { f.launcher.SetLaunchByteBudget(1, nil) })
}

func TestGroupLauncher_TaskCountCeilingIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	cfg.Tasks = nil
	for i := 0; i <= workflow.MaxGroupTasks; i++ {
		cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: fmt.Sprintf("t%d", i), Entry: fmt.Sprintf("e%d", i)})
	}

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.Error(t, err)
	assert.Nil(t, run)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, workflow.ErrGroupTooManyTasks)
	assert.Empty(t, f.groupWS.snapshotHandles())
	f.assertNothingPersisted(t)
}
