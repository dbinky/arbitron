package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// instancesNamed reads instance rows for a workflow straight from storage —
// the SINK. Unconstrained is the explicit admin-read assertion the repository
// requires before admitting rows.
func instancesNamed(t *testing.T, ds *domainStack, name string) []instance.InstanceSummary {
	t.Helper()
	all, err := ds.instanceRepo.List(context.Background(), instance.ListFilters{Unconstrained: true, Limit: 500})
	require.NoError(t, err)
	var out []instance.InstanceSummary
	for _, s := range all {
		if s.WorkflowName == name {
			out = append(out, s)
		}
	}
	return out
}

// Phase 3 admits both modes through direct, MCP and legacy-spawn triggers.
func TestGroupTriggerE2E_Phase3RunsBothGatherModes(t *testing.T) {
	t.Parallel()
	requireHostGit(t)
	ds, _ := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.Equal(t, engine.GroupSupportResultsAndFiles, ds.eng.GroupSupportLevel())

	results := filesModeGroup("results-group", "")
	results.Nodes[0].Config.Group.GatherMode = workflow.GroupGatherResults
	require.NoError(t, ds.workflowSvc.Create(ctx, "results-group", results))
	require.NoError(t, ds.workflowSvc.Create(ctx, "files-group", filesModeGroup("files-group", "default-agent")))

	assertResults := func(t *testing.T, id uuid.UUID) {
		t.Helper()
		waitForState(t, ds, id, instance.StateCompleted)
		inst, err := ds.instanceRepo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, true, inst.Variables["done"])
		for _, k := range []string{"failed", "a1", "b1"} {
			assert.NotContains(t, inst.Variables, k)
		}
	}

	t.Run("results-group runs", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "results-group", map[string]any{}, "normal")
		require.NoError(t, err)
		assertResults(t, id)
	})

	t.Run("files-group runs and gathers", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "files-group", map[string]any{}, "normal")
		require.NoError(t, err)
		assert.False(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
		assertResults(t, id)
		run := groupRunOf(t, ds, id, "fanout")
		require.NotNil(t, run.Gather)
		assert.Equal(t, "applied", run.Gather.State)
	})

	t.Run("instance_trigger over MCP runs files-group", func(t *testing.T) {
		srv := newGroupMCPServer(ds)
		res := callMCPTool(t, srv, ctx, "instance_trigger", map[string]any{"workflowName": "files-group", "priority": "normal"})
		require.False(t, res.IsError, mcpText(t, res))
		var roots []instance.InstanceSummary
		for _, run := range instancesNamed(t, ds, "files-group") {
			if run.ParentInstanceID == nil {
				roots = append(roots, run)
			}
		}
		require.Len(t, roots, 2, "direct and MCP roots; branch task rows share the workflow name")
		for _, run := range roots {
			assertResults(t, run.ID)
		}
	})

	t.Run("spawned-files-group runs as a fire-and-forget child", func(t *testing.T) {
		parent := &workflow.Workflow{
			Name: "spawns-group", Description: "spawns a group workflow", Author: "e2e",
			Nodes: []workflow.Node{
				{ID: "p1", Type: workflow.NodeTypeTransform, Entrypoint: true, Config: workflow.NodeConfig{
					Operations: []workflow.Operation{{Variable: "started", Operation: "set", Value: true}}}},
				{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
					ChildWorkflow: "files-group", SpawnMode: "fire_and_forget"}},
			},
			Edges: []workflow.Edge{{Source: "p1", Target: "spawn-1"}},
		}
		require.NoError(t, ds.workflowSvc.Create(ctx, "spawns-group", parent))
		parentID, _, err := ds.eng.Trigger(ctx, "spawns-group", map[string]any{}, "normal")
		require.NoError(t, err)
		waitForState(t, ds, parentID, instance.StateCompleted)
		var kids []instance.InstanceSummary
		require.Eventually(t, func() bool {
			kids = childrenOf(t, ds, parentID)
			return len(kids) == 1
		}, 40*time.Second, 100*time.Millisecond)
		assert.Equal(t, "files-group", kids[0].WorkflowName)
		assertResults(t, kids[0].ID)
	})

	t.Run("non-group workflow still triggers", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "group-child", map[string]any{"topic": "x"}, "normal")
		require.NoError(t, err)
		waitForState(t, ds, id, instance.StateCompleted)
	})
}
