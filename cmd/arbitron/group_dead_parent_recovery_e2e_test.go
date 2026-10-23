package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coordinator must leave files-mode finalization to the gather reconciler.
// Otherwise a dead parent freezes a results-shaped output, and a previously
// successful task decision even replays success without applying any files.
func TestGroupFilesE2E_DeadParentBeforeGatherRetriesFrozenFailure(t *testing.T) {
	for _, recovery := range []string{"startup", "periodic"} {
		for _, taskOutcome := range []string{"success", "failure"} {
			t.Run(recovery+"/"+taskOutcome, func(t *testing.T) {
				previous := groupSweepInterval
				groupSweepInterval = time.Hour
				if recovery == "periodic" {
					groupSweepInterval = 50 * time.Millisecond
				}
				t.Cleanup(func() { groupSweepInterval = previous })
				manifest, name := filesAllManifest, "grp-files-all"
				if taskOutcome == "failure" {
					manifest, name = filesFailedManifest, "grp-files-failed"
				}
				var wf map[string]any
				require.NoError(t, json.Unmarshal([]byte(manifest), &wf))
				for _, raw := range wf["nodes"].([]any) {
					node := raw.(map[string]any)
					if node["id"] == "onFailure" {
						ops := node["config"].(map[string]any)["operations"].([]any)
						for _, field := range []string{"gather.mode", "gather.state", "outcome", "taskOutcome"} {
							ops = append(ops, map[string]any{"variable": field, "operation": "set", "value": "{{group-1." + field + "}}"})
						}
						node["config"].(map[string]any)["operations"] = ops
					}
				}
				raw, err := json.Marshal(wf)
				require.NoError(t, err)
				paths := writeFilesStack(t, map[string]string{name: string(raw)})
				ctx := context.Background()
				ds, err := initStartedDomain(ctx, paths.configPath)
				require.NoError(t, err)
				t.Cleanup(func() { ds.Close(ctx) })
				// Stop at the durable decided/actions-applied boundary, using real
				// tasks, terminal receipts, snapshots and the production executor.
				// Hold both wake paths too: Shutdown drains current walkers but a
				// sweep or an in-process claim could otherwise admit another one
				// between that drain and the simulated parent death.
				_, err = ds.db.Exec(`CREATE TRIGGER test_hold_before_gather BEFORE UPDATE OF state ON group_runs
WHEN NEW.state = 'gathering'
BEGIN SELECT RAISE(ABORT, 'test crash boundary before gathering'); END;
CREATE TRIGGER test_hold_parent_wake BEFORE UPDATE OF state ON instances
WHEN OLD.state = 'waiting' AND NEW.state IN ('queued', 'running')
  AND OLD.id IN (SELECT parent_instance_id FROM group_runs)
BEGIN SELECT RAISE(ABORT, 'test crash boundary before parent wake'); END`)
				require.NoError(t, err)
				parent := triggerGroupWorkflow(t, ds, name)
				require.Eventually(t, func() bool {
					run, err := ds.groupRunRepo.GetByParentNode(ctx, parent, "group-1")
					return err == nil && run != nil && run.State == instance.GroupRunDecided && run.ActionsApplied && stateOf(t, ds, parent) == instance.StateWaiting
				}, 15*time.Second, 20*time.Millisecond)
				woke, err := adaptsqlite.NewQueueRepo(ds.db).SetQueuedIfWaitingAtNode(ctx, parent, "group-1", time.Now())
				require.ErrorContains(t, err, "test crash boundary before parent wake",
					"the crash fixture must block recovery admission before draining engine goroutines")
				require.False(t, woke)
				claimed, err := adaptsqlite.NewInstanceRepo(ds.db).ClaimWaitingParent(ctx, parent, "group-1")
				require.ErrorContains(t, err, "test crash boundary before parent wake")
				require.False(t, claimed)
				ds.eng.Shutdown()
				before := groupRunOf(t, ds, parent, "group-1")
				require.Equal(t, taskOutcome, before.Decision.Outcome)
				require.Nil(t, before.Gather)
				require.Nil(t, before.FinalOutput)
				workspace := parentVariables(t, ds, parent)[engine.ReservedVarWorkspace].(string)
				if recovery == "startup" {
					ds.Close(ctx)
					db, err := adaptsqlite.Open(filepath.Join(paths.root, "arbitron.db"))
					require.NoError(t, err)
					_, err = db.Exec(`DROP TRIGGER test_hold_parent_wake`)
					require.NoError(t, err)
					// Reconstruct the crash image: the parent was still running
					// before gathering; startup must fail it and settle the gather.
					require.NoError(t, adaptsqlite.NewInstanceRepo(db).UpdateState(ctx, parent, instance.StateRunning))
					_, err = db.Exec(`DROP TRIGGER test_hold_before_gather`)
					require.NoError(t, err)
					require.NoError(t, db.Close())
					ds, err = initStartedDomain(ctx, paths.configPath)
					require.NoError(t, err)
				} else {
					// No terminal event is emitted: only the periodic recovery input
					// can find and finish this missed callback.
					require.NoError(t, ds.instanceRepo.SetFailed(ctx, parent, "group-1"))
					_, err = ds.db.Exec(`DROP TRIGGER test_hold_before_gather`)
					require.NoError(t, err)
				}
				require.Eventually(t, func() bool {
					run := groupRunOf(t, ds, parent, "group-1")
					return run.State == instance.GroupRunDone && run.ContinuationDelivered
				}, 5*time.Second, 20*time.Millisecond)
				frozen := groupRunOf(t, ds, parent, "group-1")
				require.NotNil(t, frozen.Gather, "dead-parent settlement must retain the files-mode report")
				require.Equal(t, "failure", frozen.ContinuationPort)
				assert.Equal(t, "results_and_files", frozen.Gather.Mode)
				assert.Equal(t, "failed", frozen.Gather.State)
				assert.Contains(t, frozen.Gather.Error, "parent instance failed")
				assert.Equal(t, before.Decision, frozen.Decision, "task outcome remains immutable")
				assert.Equal(t, instance.StateFailed, stateOf(t, ds, parent))
				assert.Zero(t, nodeExecutionCount(t, ds, parent, "onFailure"))
				assert.Zero(t, nodeExecutionCount(t, ds, parent, "onSuccess"))
				bytes, err := os.ReadFile(filepath.Join(workspace, "base.txt"))
				require.NoError(t, err)
				assert.Equal(t, "base", string(bytes))
				assert.False(t, pathExists(filepath.Join(workspace, "a.txt")), "no task delta was applied")
				if recovery == "periodic" {
					_, err = ds.db.Exec(`DROP TRIGGER test_hold_parent_wake`)
					require.NoError(t, err)
				}
				require.NoError(t, ds.eng.Retry(adminCtxForLocalUser(t, ds), parent, nil))
				require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parent))
				ds.eng.Shutdown()
				assert.Equal(t, "failure", branchVariable(t, ds, parent))
				assert.Equal(t, "results_and_files", stateVar(t, ds, parent, "gather.mode"))
				assert.Equal(t, "failed", stateVar(t, ds, parent, "gather.state"))
				assert.Equal(t, "failure", stateVar(t, ds, parent, "outcome"))
				assert.Equal(t, taskOutcome, stateVar(t, ds, parent, "taskOutcome"))
				assert.Equal(t, frozen.FinalOutput, groupOutputOf(t, ds, parent, "group-1"))
				assert.Equal(t, 1, nodeExecutionCount(t, ds, parent, "onFailure"))
				assert.Zero(t, nodeExecutionCount(t, ds, parent, "onSuccess"))
				after := groupRunOf(t, ds, parent, "group-1")
				assert.Equal(t, before.ID, after.ID)
				assert.Equal(t, before.Tasks, after.Tasks, "Retry must not start a second roster")
				runs, err := ds.groupRunRepo.ListByParent(ctx, parent)
				require.NoError(t, err)
				assert.Len(t, runs, 1)
				ids, err := ds.groupRunRepo.ListUnsettled(ctx)
				require.NoError(t, err)
				assert.NotContains(t, ids, before.ID)
			})
		}
	}
}
