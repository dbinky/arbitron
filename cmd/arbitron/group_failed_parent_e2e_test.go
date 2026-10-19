package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing failed-parent groups from the recovery input must strand the slow
// task here: no child callback or direct Evaluate call can drive its cleanup.
func TestGroupBehaviorsE2E_LaterParentFailureStopsIndependentTask(t *testing.T) {
	prev := groupSweepInterval
	groupSweepInterval = 100 * time.Millisecond
	t.Cleanup(func() { groupSweepInterval = prev })

	var wf map[string]any
	require.NoError(t, json.Unmarshal([]byte(fsIndependentManifest), &wf))
	for _, raw := range wf["nodes"].([]any) {
		node := raw.(map[string]any)
		if node["id"] == "onSuccess" {
			node["type"] = "tool_call"
			node["errorBehavior"] = map[string]any{"type": "halt"}
			node["config"] = map[string]any{"command": "sh", "args": []string{"-c", "exit 1"}}
		}
	}
	manifest, err := json.Marshal(wf)
	require.NoError(t, err)
	ds, gates := bootGatedBehaviorStack(t, 5,
		map[string]string{"grp-fs-indep": string(manifest)},
		map[string][]string{"grp-fs-indep": {"slow-hold", "fast-set", "onSuccess"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-fs-indep")
	gates.reached(t, "slow-hold")
	gates.reached(t, "fast-set")
	waitForState(t, ds, parentID, instance.StateWaiting)
	gates.release(t, "fast-set")
	gates.reached(t, "onSuccess")
	slowID := taskChildID(t, ds, parentID, "race", "slow")
	run := groupRunOf(t, ds, parentID, "race")
	require.Equal(t, instance.GroupRunDone, run.State)
	require.True(t, run.ContinuationDelivered)
	require.True(t, run.ActionsApplied)
	require.True(t, groupRosterTask(t, run, "slow").ContinuingIndependently)
	require.Equal(t, instance.StateRunning, stateOf(t, ds, slowID))
	frozen := groupOutputOf(t, ds, parentID, "race")

	gates.release(t, "onSuccess") // fail naturally AFTER group delivery
	require.Equal(t, instance.StateFailed, waitForTerminalState(t, ds, parentID))
	require.Eventually(t, func() bool {
		child, err := ds.instanceRepo.Get(context.Background(), slowID)
		return err == nil && child.State == instance.StateCancelled
	}, 5*time.Second, 25*time.Millisecond, "the live recovery sweep must stop independent work under a later-failed parent")
	drainBehaviorEngine(t, ds)

	assert.Equal(t, instance.StateFailed, stateOf(t, ds, parentID))
	assert.Equal(t, frozen, groupOutputOf(t, ds, parentID, "race"))
	after := groupRunOf(t, ds, parentID, "race")
	assert.Equal(t, run.Decision, after.Decision)
	assert.Equal(t, run.FinalOutput, after.FinalOutput)
	assert.True(t, groupRosterTask(t, after, "slow").CancelRequested)
	assert.Equal(t, instance.GroupTaskStatusCancelled, groupRosterTask(t, after, "slow").LiveStatus)
	assert.Equal(t, 1, nodeExecutionCount(t, ds, parentID, "onSuccess"))
	assert.Equal(t, 0, nodeExecutionCount(t, ds, parentID, "onFailure"))
	assert.Equal(t, 0, nodeExecutionCount(t, ds, slowID, "slow-set"))
	ids, err := ds.groupRunRepo.ListUnsettled(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, ids, run.ID, "the completed cleanup must leave the recovery input")
}
