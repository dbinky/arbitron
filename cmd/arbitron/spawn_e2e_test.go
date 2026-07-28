package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localDefaultUser is the admin seeded by migration 001; in local auth mode it
// owns every run and every private workflow.
var localDefaultUser = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// writeWorkflow writes a workflow.json into the default user's private scope.
func writeWorkflow(t *testing.T, workspaceDir, name, manifest string) {
	t.Helper()
	dir := filepath.Join(workspaceDir, "users", localDefaultUser.String(), name)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(manifest), 0o600))
}

// bootSpawnStack writes a parent + child workflow pair and boots the REAL
// composition root against them.
func bootSpawnStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	// The child declares ZERO inputs, so the preparer short-circuits and no
	// provider call is ever made — the spawn path runs on real wiring with no LLM.
	writeWorkflow(t, workspaceDir, "spawn-child", `{
  "name": "spawn-child",
  "description": "child",
  "inputs": [],
  "nodes": [
    {"id": "c1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}
  ],
  "edges": []
}`)

	writeWorkflow(t, workspaceDir, "spawn-parent", `{
  "name": "spawn-parent",
  "description": "parent",
  "inputs": [],
  "nodes": [
    {"id": "p1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "spawn-1", "type": "spawn_workflow",
     "config": {"childWorkflow": "spawn-child", "spawnMode": "fire_and_forget"}},
    {"id": "p2", "type": "transform",
     "config": {"operations": [{"variable": "finished", "operation": "set", "value": true}]}}
  ],
  "edges": [
    {"source": "p1", "target": "spawn-1"},
    {"source": "spawn-1", "target": "p2"}
  ]
}`)

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err, "the composition root must boot with a spawn workflow present")
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// childrenOf returns the persisted children of parentID, read straight from the
// repository — the SINK, not an intermediate hop.
func childrenOf(t *testing.T, ds *domainStack, parentID uuid.UUID) []instance.InstanceSummary {
	t.Helper()
	got, err := ds.instanceRepo.List(context.Background(), instance.ListFilters{
		ParentScope:      instance.ParentScopeChildren,
		ParentInstanceID: &parentID,
		Limit:            50,
	})
	require.NoError(t, err)
	return got
}

// PROVENANCE PROBE. This is the test that fails when the feature is inert: if
// the spawn executor is not registered at the composition root, the engine
// reports "no executor registered for node type: spawn_workflow", the parent
// fails, and NO child row is ever written.
func TestSpawnE2E_FireAndForgetCreatesLinkedChildThroughCompositionRoot(t *testing.T) {
	ds := bootSpawnStack(t)

	parentID, _, err := ds.eng.Trigger(context.Background(), "spawn-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	var children []instance.InstanceSummary
	require.Eventually(t, func() bool {
		children = childrenOf(t, ds, parentID)
		return len(children) == 1
	}, 20*time.Second, 100*time.Millisecond,
		"exactly one linked child must be created by the spawn node running through the real engine")

	child := children[0]
	assert.Equal(t, "spawn-child", child.WorkflowName)
	require.NotNil(t, child.ParentInstanceID)
	assert.Equal(t, parentID, *child.ParentInstanceID)

	full, err := ds.instanceRepo.Get(context.Background(), child.ID)
	require.NoError(t, err)
	require.NotNil(t, full.ParentNodeID)
	assert.Equal(t, "spawn-1", *full.ParentNodeID,
		"the link must name the SPECIFIC spawn node, not just the parent run")
	assert.Equal(t, instance.SpawnModeFireAndForget, full.SpawnMode)

	parent, err := ds.instanceRepo.Get(context.Background(), parentID)
	require.NoError(t, err)
	assert.Equal(t, parent.OwnerID, full.OwnerID, "the child inherits the parent instance's owner")
}

// The parent CONTINUES past a fire-and-forget spawn and reaches its terminal
// state — the node returns a non-wait result and the engine follows the single
// unnamed edge.
func TestSpawnE2E_ParentContinuesAndCompletes(t *testing.T) {
	ds := bootSpawnStack(t)

	parentID, _, err := ds.eng.Trigger(context.Background(), "spawn-parent", map[string]any{}, "normal")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(context.Background(), parentID)
		return gerr == nil && inst.State == instance.StateCompleted
	}, 20*time.Second, 100*time.Millisecond,
		"the parent must advance past the spawn node and complete")

	// The node output records the child id so downstream nodes and the trace can
	// reference the spawned run.
	exec, err := ds.nodeExecRepo.GetByInstanceAndNode(context.Background(), parentID, "spawn-1")
	require.NoError(t, err)
	require.NotNil(t, exec)
	assert.Equal(t, instance.NodeStatusSucceeded, exec.Status)
	assert.NotEmpty(t, exec.Output["childInstanceId"])
	assert.Equal(t, "spawn-child", exec.Output["childWorkflow"])
}

// A run with no spawn node is a ROOT and has no children — proving the parent
// filter is not vacuously matching everything.
func TestSpawnE2E_ChildlessRunIsARootWithNoChildren(t *testing.T) {
	ds := bootSpawnStack(t)

	childID, _, err := ds.eng.Trigger(context.Background(), "spawn-child", map[string]any{}, "normal")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(context.Background(), childID)
		return gerr == nil && inst.State == instance.StateCompleted
	}, 20*time.Second, 100*time.Millisecond)

	inst, err := ds.instanceRepo.Get(context.Background(), childID)
	require.NoError(t, err)
	assert.True(t, inst.IsRoot())
	assert.Empty(t, childrenOf(t, ds, childID))
}
