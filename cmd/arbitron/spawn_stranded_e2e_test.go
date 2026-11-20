package main

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The 2026-11-19 incident on real wiring: a feature-pipeline parent was left
// `running` at its awaited spawn node with no walker (a pre-fix resume while
// its child was alive). When the child failed, the in-process wake skipped the
// non-waiting parent, and nothing else would ever move it; it held a
// concurrency slot until someone cancelled it.
//
// Here the parent is stranded the same way, by writing `running` over its
// `waiting` row with no walker behind it. Only the periodic spawn sweep can
// rescue it: it must adopt the parent and route it through its FAILURE port.
// Removing WithStrandedAdoption from initDomain leaves the parent running.
//
// Not parallel: it shortens the package-level sweep interval.
func TestSpawnStrandedE2E_ParentStrandedInRunning_FailedChildRoutesParentToFailurePath(t *testing.T) {
	prev := spawnSweepInterval
	spawnSweepInterval = 200 * time.Millisecond
	t.Cleanup(func() { spawnSweepInterval = prev })

	ds := bootStartedWaitStack(t, "fail", 1, "3")
	ctx := context.Background()

	parentID, _, err := ds.eng.Trigger(ctx, "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, parentID, instance.StateWaiting)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)

	// Strand it: running at spawn-1, no walker.
	require.NoError(t, ds.instanceRepo.UpdateState(ctx, parentID, instance.StateRunning))

	waitForState(t, ds, children[0].ID, instance.StateFailed)
	waitForState(t, ds, parentID, instance.StateCompleted)
	assert.Equal(t, "failure", branchVariable(t, ds, parentID),
		"the adopted parent must take its failure path, not be failed as a crash")
}

// The same rescue with a COMPLETED child: the adopted parent must take its
// SUCCESS branch (reading the child's output), not the failure path or a crash
// failure. Pins that adoption is outcome-agnostic on real wiring.
//
// Not parallel: it shortens the package-level sweep interval.
func TestSpawnStrandedE2E_ParentStrandedInRunning_CompletedChildRoutesParentToSuccessPath(t *testing.T) {
	prev := spawnSweepInterval
	spawnSweepInterval = 200 * time.Millisecond
	t.Cleanup(func() { spawnSweepInterval = prev })

	ds := bootStartedWaitStack(t, "complete", 1, "3")
	ctx := context.Background()

	parentID, _, err := ds.eng.Trigger(ctx, "wait-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, parentID, instance.StateWaiting)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)

	// Strand it: running at spawn-1, no walker.
	require.NoError(t, ds.instanceRepo.UpdateState(ctx, parentID, instance.StateRunning))

	waitForState(t, ds, children[0].ID, instance.StateCompleted)
	waitForState(t, ds, parentID, instance.StateCompleted)
	assert.Equal(t, "success", branchVariable(t, ds, parentID),
		"the adopted parent must take its success branch")
}
