package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gsWorkflow is an entrypoint group (mode = gatherMode) continuing to a script node.
func gsWorkflow(name, mode string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name,
		Nodes: []workflow.Node{
			{ID: "fanout", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedCancel,
				GatherMode:         mode,
				ResolverProfile:    "default-agent",
			}}},
			{ID: "a1", Type: workflow.NodeTypeScript},
			{ID: "done", Type: workflow.NodeTypeScript},
			{ID: "failed", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "fanout", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "fanout", Target: "done", Output: workflow.GroupPortSuccess},
			{Source: "fanout", Target: "failed", Output: workflow.GroupPortFailure},
		},
	}
}

func gsInstanceCount(f *testFixtures) int {
	f.instanceRepo.mu.Lock()
	defer f.instanceRepo.mu.Unlock()
	return len(f.instanceRepo.instances)
}

func TestEngine_GroupSupport_DefaultLevelIsNone(t *testing.T) {
	f := newTestFixtures()
	assert.Equal(t, engine.GroupSupportNone, f.engine.GroupSupportLevel())
	f.engine.SetGroupSupport(engine.GroupSupportResults)
	assert.Equal(t, engine.GroupSupportResults, f.engine.GroupSupportLevel())
}

func TestEngine_Trigger_GroupWorkflowRefusedByDefault(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(gsWorkflow("group-wf", workflow.GroupGatherResults))

	id, started, err := f.engine.Trigger(context.Background(), "group-wf", nil, "normal")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Contains(t, err.Error(), `node "fanout"`)
	assert.Contains(t, err.Error(), "this server cannot execute parallel_group yet")
	assert.Equal(t, uuid.Nil, id)
	assert.False(t, started)
	assert.Equal(t, 0, gsInstanceCount(f), "no instance row may be created")
}

func TestEngine_Trigger_GuardRunsBeforeInputValidation(t *testing.T) {
	f := newTestFixtures()
	wf := gsWorkflow("group-with-inputs", workflow.GroupGatherResults)
	wf.Inputs = []workflow.InputField{{Name: "topic", Type: "string", Required: true}}
	f.workflowRepo.add(wf)

	_, _, err := f.engine.Trigger(context.Background(), "group-with-inputs", nil, "normal")
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported),
		"the capability refusal wins over 'missing required input' so the author fixes the real blocker")
}

func TestEngine_Trigger_ResultsLevelAllowsResultsGroups(t *testing.T) {
	f := newTestFixtures()
	f.engine.RegisterExecutor(workflow.NodeTypeParallelGroup, f.executor)
	f.engine.SetGroupSupport(engine.GroupSupportResults)
	f.workflowRepo.add(gsWorkflow("group-wf", workflow.GroupGatherResults))

	id, _, err := f.engine.Trigger(context.Background(), "group-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	assert.Equal(t, "group-wf", inst.WorkflowName)
}

func TestEngine_Trigger_ResultsLevelRefusesFileGathering(t *testing.T) {
	f := newTestFixtures()
	f.engine.SetGroupSupport(engine.GroupSupportResults)
	f.workflowRepo.add(gsWorkflow("files-wf", workflow.GroupGatherResultsAndFiles))

	_, _, err := f.engine.Trigger(context.Background(), "files-wf", nil, "normal")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Contains(t, err.Error(), "file gathering is not available yet")
	assert.Equal(t, 0, gsInstanceCount(f), "a files-requesting group is never run as results-only")
}

func TestEngine_Trigger_ResultsAndFilesAllowsEveryGroup(t *testing.T) {
	f := newTestFixtures()
	f.engine.RegisterExecutor(workflow.NodeTypeParallelGroup, f.executor)
	f.engine.SetGroupSupport(engine.GroupSupportResultsAndFiles)
	f.workflowRepo.add(gsWorkflow("files-wf", workflow.GroupGatherResultsAndFiles))

	id, _, err := f.engine.Trigger(context.Background(), "files-wf", nil, "normal")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
}

func TestEngine_Trigger_NonGroupWorkflowUnaffected(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
}

func TestEngine_Rerun_GroupWorkflowRefused(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(gsWorkflow("group-wf", workflow.GroupGatherResults))
	src := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "group-wf",
		State: instance.StateCompleted, Priority: instance.Priority("normal"),
		Variables: map[string]any{}, OriginalVariables: map[string]any{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), src))

	newID, err := f.engine.Rerun(bgAdmin(), src.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Equal(t, uuid.Nil, newID)
	assert.Equal(t, 1, gsInstanceCount(f), "rerun creates nothing")
}

func TestEngine_Spawn_GroupChildRefused(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(gsWorkflow("group-child", workflow.GroupGatherResults))
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "parent",
		State: instance.StateRunning, Priority: instance.Priority("normal"),
		Variables: map[string]any{}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), parent))

	_, err := f.engine.Spawn(bgAdmin(), engine.SpawnRequest{
		ChildWorkflow: "group-child",
		Priority:      "normal",
		Link:          instance.SpawnLink{ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Equal(t, 1, gsInstanceCount(f), "no child row")
}
