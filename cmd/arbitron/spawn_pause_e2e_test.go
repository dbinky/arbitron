package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reported bug, on real wiring: pausing a parent that is waiting on a
// running child must pause the child too, and resuming the parent must resume
// it so the whole tree finishes.
func TestSpawnPauseE2E_PausingParentPausesChild_ResumingParentFinishesTree(t *testing.T) {
	t.Parallel()
	ds := bootWaitStack(t, "complete")
	ctx := adminCtxForLocalUser(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, parentID, instance.StateWaiting)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)
	childID := children[0].ID
	waitForState(t, ds, childID, instance.StateRunning)

	require.NoError(t, ds.eng.Pause(ctx, parentID))

	waitForState(t, ds, parentID, instance.StatePaused)
	waitForState(t, ds, childID, instance.StatePaused)

	require.NoError(t, ds.eng.Resume(ctx, parentID))

	waitForState(t, ds, childID, instance.StateCompleted)
	waitForState(t, ds, parentID, instance.StateCompleted)
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
}

// A child that finishes while its awaiting parent is paused cannot wake it
// (the wake only acts on a WAITING parent). Resuming the parent must still
// pick up the finished child's outcome and route through SUCCESS.
func TestSpawnPauseE2E_ChildFinishesWhileParentPaused_ResumeRoutesSuccess(t *testing.T) {
	t.Parallel()
	ds := bootWaitStack(t, "complete")
	ctx := adminCtxForLocalUser(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, parentID, instance.StateWaiting)
	childID := childrenOf(t, ds, parentID)[0].ID
	waitForState(t, ds, childID, instance.StateRunning)

	require.NoError(t, ds.eng.Pause(ctx, parentID))
	waitForState(t, ds, childID, instance.StatePaused)

	// The operator resumes only the child; it runs to completion while the
	// parent stays paused.
	require.NoError(t, ds.eng.Resume(ctx, childID))
	waitForState(t, ds, childID, instance.StateCompleted)
	parent, err := ds.instanceRepo.Get(context.Background(), parentID)
	require.NoError(t, err)
	require.Equal(t, instance.StatePaused, parent.State, "a finished child must not wake a paused parent")

	require.NoError(t, ds.eng.Resume(ctx, parentID))

	waitForState(t, ds, parentID, instance.StateCompleted)
	assert.Equal(t, "success", branchVariable(t, ds, parentID),
		"the resumed parent routes through SUCCESS with the child's output")
}
