package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stampFixture registers a trivial one-node workflow so Trigger succeeds, and
// returns the fixtures.
func stampFixture(t *testing.T) *testFixtures {
	t.Helper()
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "child",
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})
	return f
}

// Happy path: a trigger whose context carries a spawn link creates the instance
// with the link ALREADY SET on the row observed by Create.
func TestTrigger_StampsSpawnLinkAtConstruction(t *testing.T) {
	f := stampFixture(t)
	parentID := uuid.New()

	ctx := instance.WithSpawnLink(context.Background(), instance.SpawnLink{
		ParentInstanceID: parentID,
		ParentNodeID:     "spawn-1",
		Mode:             instance.SpawnModeWaitForChild,
	})

	childID, _, err := f.engine.Trigger(ctx, "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	require.NotNil(t, got.ParentInstanceID,
		"the link must be present on the instance handed to Create, not written afterwards")
	assert.Equal(t, parentID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeWaitForChild, got.SpawnMode)
}

// A plain trigger (no spawn link on the context) creates a ROOT run.
func TestTrigger_NoSpawnLinkCreatesRoot(t *testing.T) {
	f := stampFixture(t)

	childID, _, err := f.engine.Trigger(context.Background(), "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.True(t, got.IsRoot())
	assert.Equal(t, instance.SpawnMode(""), got.SpawnMode)
}

// A PARTIAL link is never stamped: a row with a parent id but no node id could
// not be matched by the wake and would strand its parent forever.
func TestTrigger_PartialSpawnLinkIsNotStamped(t *testing.T) {
	f := stampFixture(t)

	ctx := instance.WithSpawnLink(context.Background(), instance.SpawnLink{
		ParentInstanceID: uuid.New(), // no node id, no mode
	})
	childID, _, err := f.engine.Trigger(ctx, "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.True(t, got.IsRoot(), "an incomplete link must not be persisted at all")
}

// The spawn link and the run origin are stamped independently.
func TestTrigger_SpawnLinkAndOriginCoexist(t *testing.T) {
	f := stampFixture(t)
	sid := uuid.New()
	parentID := uuid.New()

	ctx := instance.WithOrigin(context.Background(), instance.Origin{
		Source: instance.OriginSchedule, ScheduleID: &sid,
	})
	ctx = instance.WithSpawnLink(ctx, instance.SpawnLink{
		ParentInstanceID: parentID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
	})

	childID, _, err := f.engine.Trigger(ctx, "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.Equal(t, instance.OriginSchedule, got.TriggerSource)
	require.NotNil(t, got.ScheduleID)
	assert.Equal(t, sid, *got.ScheduleID)
	require.NotNil(t, got.ParentInstanceID)
	assert.Equal(t, parentID, *got.ParentInstanceID)
}
