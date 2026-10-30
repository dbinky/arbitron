package main

import (
	"context"
	"database/sql/driver"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlitedriver "modernc.org/sqlite"
)

// The subprocess exits inside the first parent-variable write after deliver's
// marker committed. SQLite rolls back that write; no graceful shutdown can
// persist the output or advance the cursor. All fault injection stays in tests.
func TestGroupDeliveryCrashProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_GROUP_DELIVERY_CRASH_CONFIG")
	if configPath == "" {
		return
	}
	require.NoError(t, sqlitedriver.RegisterScalarFunction("test_group_delivery_crash", 0,
		func(*sqlitedriver.FunctionContext, []driver.Value) (driver.Value, error) {
			os.Exit(86)
			return nil, nil
		}))
	ds, err := initStartedDomain(context.Background(), configPath)
	require.NoError(t, err)
	_, err = ds.db.Exec(`CREATE TRIGGER test_group_delivery_crash
		BEFORE UPDATE OF variables ON instances
		WHEN EXISTS (SELECT 1 FROM group_runs g
			WHERE g.parent_instance_id = NEW.id AND g.node_id = NEW.current_node_id
			AND g.continuation_delivered = 1)
		BEGIN SELECT test_group_delivery_crash(); END`)
	require.NoError(t, err)
	parent := triggerGroupWorkflow(t, ds, os.Getenv("ARBITRON_GROUP_DELIVERY_CRASH_WORKFLOW"))
	waitForTerminalState(t, ds, parent)
	t.Fatal("the parent never reached the delivery/persistence crash point")
}

// Refusing to replay a delivered run, returning empty output, or changing the
// frozen port must fail at downstream consumers, not merely at a wake marker.
func TestGroupE2E_DeliveryCrashRetryConsumesFrozenOutputOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, workflow, node, manifest, port string
	}{
		{"success", "grp-branches", "group-1", groupBranchesManifest, "success"},
		{"failure", "grp-as-wait", "all", strings.ReplaceAll(asWaitManifest, "sleep 5; ", ""), "failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := writeGroupStack(t, 5, map[string]string{tc.workflow: tc.manifest})
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGroupDeliveryCrashProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "ARBITRON_GROUP_DELIVERY_CRASH_CONFIG="+paths.configPath,
				"ARBITRON_GROUP_DELIVERY_CRASH_WORKFLOW="+tc.workflow)
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, "subprocess output: %s", output)
			require.Equal(t, 86, exitErr.ExitCode(), "subprocess output: %s", output)

			// Inspect the actual crash image BEFORE initDomain recovers it.
			db, err := adaptsqlite.Open(filepath.Join(paths.root, "arbitron.db"))
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			var rawParent string
			require.NoError(t, db.QueryRow(`SELECT id FROM instances WHERE parent_instance_id IS NULL`).Scan(&rawParent))
			parent := uuid.MustParse(rawParent)
			inst, err := adaptsqlite.NewInstanceRepo(db).Get(ctx, parent)
			require.NoError(t, err)
			require.Equal(t, instance.StateRunning, inst.State)
			require.NotNil(t, inst.CurrentNodeID)
			require.Equal(t, tc.node, *inst.CurrentNodeID)
			require.NotContains(t, inst.Variables, tc.node, "group output was not persisted")
			frozen, err := adaptsqlite.NewGroupRunRepo(db).GetByParentNode(ctx, parent, tc.node)
			require.NoError(t, err)
			require.NotNil(t, frozen)
			require.True(t, frozen.ContinuationDelivered, "deliver's marker committed before the crash")
			require.Equal(t, tc.port, frozen.ContinuationPort)
			require.Len(t, frozen.Tasks, 2)
			_, err = db.Exec(`DROP TRIGGER test_group_delivery_crash`)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			ds, err := initStartedDomain(ctx, paths.configPath)
			require.NoError(t, err)
			t.Cleanup(func() { ds.Close(context.Background()) })
			inst, err = ds.instanceRepo.Get(ctx, parent)
			require.NoError(t, err)
			require.Equal(t, instance.StateFailed, inst.State, "startup fails the crashed running parent; Retry is explicit")
			require.NotNil(t, inst.FailedNodeID)
			require.Equal(t, tc.node, *inst.FailedNodeID)
			assert.Equal(t, 0, nodeExecutionCount(t, ds, parent, "onSuccess"))
			assert.Equal(t, 0, nodeExecutionCount(t, ds, parent, "onFailure"))
			require.NoError(t, ds.eng.Retry(adminCtxForLocalUser(t, ds), parent, nil))
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parent))
			ds.eng.Shutdown()

			assert.Equal(t, tc.port, branchVariable(t, ds, parent))
			assert.Equal(t, frozen.FinalOutput, groupOutputOf(t, ds, parent, tc.node))
			if tc.port == "success" {
				assert.Equal(t, "alpha-done", stateVar(t, ds, parent, "consumedFirst"))
				assert.Equal(t, "beta-done", stateVar(t, ds, parent, "consumedSecond"))
				assert.Equal(t, "beta-done", stateVar(t, ds, parent, "consumedByKey"))
				assert.Equal(t, 1, nodeExecutionCount(t, ds, parent, "onSuccess"))
				assert.Equal(t, 0, nodeExecutionCount(t, ds, parent, "onFailure"))
			} else {
				assert.Equal(t, "late", stateVar(t, ds, parent, "consumedFirstResult"))
				assert.Equal(t, "failed", stateVar(t, ds, parent, "consumedSecondStatus"))
				assert.Equal(t, "completed", stateVar(t, ds, parent, "consumedByKeyStatus"))
				assert.Equal(t, 0, nodeExecutionCount(t, ds, parent, "onSuccess"))
				assert.Equal(t, 1, nodeExecutionCount(t, ds, parent, "onFailure"))
			}
			after := groupRunOf(t, ds, parent, tc.node)
			assert.Equal(t, frozen.ID, after.ID)
			assert.Equal(t, frozen.Decision, after.Decision)
			assert.Equal(t, frozen.Tasks, after.Tasks, "retry cannot relaunch children or rewrite receipts")
			runs, err := ds.groupRunRepo.ListByParent(ctx, parent)
			require.NoError(t, err)
			assert.Len(t, runs, 1)
		})
	}
}
