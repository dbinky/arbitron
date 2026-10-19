package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing the Retry group-task guard must expose both the raw terminal-trigger
// error and the variable/cursor/dismissal mutations that happen before it.
func TestGroupE2E_RetryFailedTaskIsRejectedWithoutSideEffects(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-as-wait": asWaitManifest},
		map[string][]string{"grp-as-wait": {"slow-hold", "bad-run"}})
	ctx := context.Background()
	parent := triggerGroupWorkflow(t, ds, "grp-as-wait")
	gates.reached(t, "slow-hold")
	gates.reached(t, "bad-run")
	waitForState(t, ds, parent, instance.StateWaiting)
	gates.release(t, "bad-run")
	bad := taskChildID(t, ds, parent, "all", "bad")
	waitForState(t, ds, bad, instance.StateFailed)
	waitForLiveStatus(t, ds, parent, "all", "bad", instance.GroupTaskStatusFailed)
	// A rejected request must preserve a retained cursor even if it differs
	// from failed_node_id, rather than resetting it before SQLite rejects state.
	require.NoError(t, ds.instanceRepo.UpdateCurrentNode(ctx, bad, "retained-cursor"))
	before, err := ds.instanceRepo.Get(ctx, bad)
	require.NoError(t, err)
	require.True(t, before.IsGroupTask(), "the production child link feeds the retry guard")
	require.NoError(t, ds.dismissalRepo.Dismiss(ctx, before.OwnerID, bad))
	auth := adminCtxForLocalUser(t, ds)

	for _, decided := range []bool{false, true} {
		if decided {
			gates.release(t, "slow-hold")
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parent))
			ds.eng.Shutdown()
		}
		run := groupRunOf(t, ds, parent, "all")
		assert.Equal(t, decided, run.Decision != nil)
		for _, edits := range []map[string]any{nil, {"retry_injected": "must not persist"}} {
			err := ds.eng.Retry(auth, bad, edits)
			assert.ErrorIs(t, err, instance.ErrCannotRetry, "return a domain refusal before SQLite's immutable-state trigger")
			after, loadErr := ds.instanceRepo.Get(ctx, bad)
			require.NoError(t, loadErr)
			assert.Equal(t, before, after, "rejection must preserve variables, cursor, timestamps and terminal state")
			dismissed, loadErr := ds.dismissalRepo.DismissedIDs(ctx, before.OwnerID)
			require.NoError(t, loadErr)
			assert.True(t, dismissed[bad], "a rejected retry must not clear dismissals")
			assert.Equal(t, run, groupRunOf(t, ds, parent, "all"), "receipt and frozen decision stay untouched")
		}
	}
	assert.Equal(t, "failure", branchVariable(t, ds, parent))
	assert.Equal(t, "failed", stateVar(t, ds, parent, "consumedSecondStatus"))
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parent, "onFailure"))
	assert.Equal(t, 0, nodeExecutionCount(t, ds, parent, "onSuccess"))
}
