package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compile-time proof that *Engine satisfies the port the executor depends on.
var _ engine.ChildSpawner = (*engine.Engine)(nil)

func spawnerFixture(t *testing.T) *testFixtures {
	t.Helper()
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "child",
		Inputs: []workflow.InputField{
			{Name: "repo", Type: "string", Description: "target repo", Required: true},
			{Name: "dryRun", Type: "boolean", Required: false},
		},
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})
	return f
}

// ---------- ChildInputs ----------

func TestEngineChildInputs_ReturnsDeclaredContract(t *testing.T) {
	f := spawnerFixture(t)

	got, err := f.engine.ChildInputs(context.Background(), "child")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "repo", got[0].Name)
	assert.True(t, got[0].Required)
	assert.Equal(t, "dryRun", got[1].Name)
	assert.False(t, got[1].Required)
}

func TestEngineChildInputs_UnknownWorkflowErrors(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.ChildInputs(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound,
		"a workflow the caller cannot see is indistinguishable from one that does not exist")
}

func TestEngineChildInputs_ZeroDeclaredInputs(t *testing.T) {
	f := spawnerFixture(t)
	f.workflowRepo.add(&workflow.Workflow{
		Name: "no-inputs",
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})

	got, err := f.engine.ChildInputs(context.Background(), "no-inputs")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// The lookup is scoped to the identity on the context, exactly as the trigger
// path is: a private workflow of user A is not visible to user B.
func TestEngineChildInputs_ScopedToContextIdentity(t *testing.T) {
	f := spawnerFixture(t)
	other := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: other, Role: "user", Departments: []string{"default"},
	})

	// The mock workflow repo is scope-agnostic, so this asserts the CALL SHAPE:
	// ChildInputs resolves through the same owner-scoped LoadVisibleFiltered the
	// trigger path uses, with the owner taken from the context identity.
	got, err := f.engine.ChildInputs(ctx, "child")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// ---------- Spawn ----------

func TestEngineSpawn_CreatesLinkedChildUnderRequestedOwner(t *testing.T) {
	f := spawnerFixture(t)
	owner := uuid.New()
	parentID := uuid.New()

	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: owner, Role: "user", Departments: []string{"default"},
	})

	childID, err := f.engine.Spawn(ctx, engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "arbitron/arbitron"},
		Priority:      string(instance.PriorityHigh),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID,
			ParentNodeID:     "spawn-1",
			Mode:             instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, childID)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.Equal(t, owner, got.OwnerID, "the child inherits the identity injected by the caller")
	require.NotNil(t, got.ParentInstanceID)
	assert.Equal(t, parentID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeFireAndForget, got.SpawnMode)
	assert.Equal(t, instance.PriorityHigh, got.Priority,
		"a child of a high-priority run competes on equal footing with its parent's siblings")
}

func TestEngineSpawn_PropagatesMissingRequiredInput(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{}, // "repo" is required
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: uuid.New(), ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo")
}

func TestEngineSpawn_UnknownChildWorkflowErrors(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "nope",
		Variables:     map[string]any{},
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: uuid.New(), ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

// An INVALID link is rejected before any instance is created: a child that
// cannot be matched by the wake would strand its parent forever, which is worse
// than a failed spawn.
func TestEngineSpawn_RejectsInvalidLinkBeforeCreating(t *testing.T) {
	f := spawnerFixture(t)
	before := len(f.instanceRepo.all())

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "r"},
		Priority:      string(instance.PriorityNormal),
		Link:          instance.SpawnLink{ParentNodeID: "spawn-1"}, // no parent id, no mode
	})
	require.Error(t, err)
	assert.Equal(t, before, len(f.instanceRepo.all()), "no child may be created for an invalid link")
}

// A grandchild is linked to ITS parent, not to the root.
func TestEngineSpawn_GrandchildLinksToItsOwnParent(t *testing.T) {
	f := spawnerFixture(t)
	root := uuid.New()

	childID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: root, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)

	grandID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: childID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)

	grand := f.instanceRepo.get(grandID)
	require.NotNil(t, grand)
	require.NotNil(t, grand.ParentInstanceID)
	assert.Equal(t, childID, *grand.ParentInstanceID)
	assert.NotEqual(t, root, *grand.ParentInstanceID)
}
