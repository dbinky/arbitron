package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These faults happen after the real launcher has committed and enqueued its
// roster. Applying skip/route, or retrying in executeWithRetry, must not bypass
// the outstanding group. A process gate holds one real task until we inspect
// the parked parent, then the normal terminal callback completes the same run.
func TestGroupE2E_PostLaunchWriteFailureBlocksErrorBehaviorUntilRecovered(t *testing.T) {
	t.Parallel()
	faults := []struct {
		name string
		sql  string
	}{
		{"wait_trace", `CREATE TRIGGER test_launch_write_failure BEFORE INSERT ON node_executions
WHEN NEW.node_id = 'group-1' AND NEW.status = 'succeeded'
 AND json_extract(NEW.output, '$.state') IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'test post-launch write unavailable'); END`},
		// The failed attempt's diagnostic trace makes this a transient failure:
		// suspend fails, but the engine can subsequently park the parent.
		{"waiting_state", `CREATE TRIGGER test_launch_write_failure BEFORE UPDATE OF state ON instances
WHEN NEW.current_node_id = 'group-1' AND NEW.state = 'waiting'
 AND NOT EXISTS (SELECT 1 FROM node_executions
 WHERE instance_id = NEW.id AND node_id = 'group-1' AND status = 'failed')
BEGIN SELECT RAISE(ABORT, 'test post-launch write unavailable'); END`},
	}
	for _, fault := range faults {
		for _, behavior := range []string{"skip", "route", "retry"} {
			t.Run(fault.name+"/"+behavior, func(t *testing.T) {
				var manifest map[string]any
				require.NoError(t, json.Unmarshal([]byte(groupCrashManifest), &manifest))
				for _, raw := range manifest["nodes"].([]any) {
					node := raw.(map[string]any)
					if node["id"] == "group-1" {
						node["errorBehavior"] = map[string]any{"type": behavior, "errorNodeId": "onFailure", "retries": 2}
					}
					if node["id"] == "onSuccess" {
						config := node["config"].(map[string]any)
						config["operations"] = append(config["operations"].([]any),
							map[string]any{"variable": "consumedQuick", "operation": "set", "value": "{{group-1.tasks.0.result.quick_value}}"},
							map[string]any{"variable": "consumedSlow", "operation": "set", "value": "{{group-1.tasks.1.result.gateDir}}"})
					}
				}
				raw, err := json.Marshal(manifest)
				require.NoError(t, err)
				ds, _ := bootGroupExecStack(t, 1, map[string]string{"grp-crash": string(raw)})
				gate := t.TempDir()
				release := func() { require.NoError(t, os.WriteFile(filepath.Join(gate, "slow.release"), nil, 0o600)) }
				t.Cleanup(release) // release even when an assertion fails, before closing the stack
				_, err = ds.db.Exec(fault.sql)
				require.NoError(t, err)
				ctx := context.Background()
				id, _, err := ds.eng.Trigger(ctx, "grp-crash", map[string]any{"gateDir": gate}, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					parent, err := ds.instanceRepo.Get(ctx, id)
					if err != nil {
						return false
					}
					if parent.State.IsTerminal() {
						return true // expose unsafe skip/route without a long timeout
					}
					_, err = os.Stat(filepath.Join(gate, "slow.reached"))
					return parent.State == instance.StateWaiting && err == nil
				}, 5*time.Second, 10*time.Millisecond)
				parent, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, instance.StateWaiting, parent.State, "post-launch errors must bypass node ErrorBehavior")
				require.NotNil(t, parent.CurrentNodeID)
				assert.Equal(t, "group-1", *parent.CurrentNodeID)
				assert.NotContains(t, parent.Variables, "branch")
				assert.Zero(t, nodeExecutionCount(t, ds, id, "onSuccess"))
				assert.Zero(t, nodeExecutionCount(t, ds, id, "onFailure"))
				run := groupRunOf(t, ds, id, "group-1")
				require.Len(t, run.Tasks, 2)
				assert.False(t, run.ContinuationDelivered)
				slow, err := ds.instanceRepo.Get(ctx, run.Tasks[1].ChildInstanceID)
				require.NoError(t, err)
				assert.Equal(t, instance.StateRunning, slow.State, "the waiting parent must release the sole owner slot")
				rows, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				failedAttempts := 0
				for _, row := range rows {
					if row.NodeID == "group-1" && row.Status == instance.NodeStatusFailed {
						failedAttempts++
						assert.Contains(t, row.Error["error"], "test post-launch write unavailable")
					}
				}
				assert.Equal(t, 1, failedAttempts, "the coordinator, rather than node retry, owns recovery")

				_, err = ds.db.Exec(`DROP TRIGGER test_launch_write_failure`)
				require.NoError(t, err)
				release()
				require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, id))
				vars := parentVariables(t, ds, id)
				assert.Equal(t, "success", vars["branch"])
				assert.Equal(t, "q", vars["consumedQuick"])
				assert.Equal(t, gate, vars["consumedSlow"], "downstream must consume the actual launched task's input")
				assert.Equal(t, 1, nodeExecutionCount(t, ds, id, "onSuccess"))
				assert.Zero(t, nodeExecutionCount(t, ds, id, "onFailure"))
				final := groupRunOf(t, ds, id, "group-1")
				assert.Equal(t, run.ID, final.ID)
				require.Len(t, final.Tasks, 2)
				for i, task := range final.Tasks {
					assert.Equal(t, run.Tasks[i].ChildInstanceID, task.ChildInstanceID)
					assert.Equal(t, instance.GroupTaskStatusCompleted, task.LiveStatus)
				}
				runs, err := ds.groupRunRepo.ListByParent(ctx, id)
				require.NoError(t, err)
				assert.Len(t, runs, 1)
				assert.True(t, final.ContinuationDelivered)
			})
		}
	}
}
